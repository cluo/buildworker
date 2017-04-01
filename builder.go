package buildworker

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/crypto/openpgp"
)

// Signer is the entity which can sign builds.
// Its private key must be decrypted.
var Signer *openpgp.Entity

// TODO: Maintain master gopath (when? master gopaths are
// scoped to individual BuildEnvs) by pruning unused packages...

var gopathLocks = make(map[string]*sync.RWMutex)

func lock(gopath string) {
	if _, ok := gopathLocks[gopath]; !ok {
		gopathLocks[gopath] = new(sync.RWMutex)
	}
	gopathLocks[gopath].Lock()
}

func unlock(gopath string) {
	gopathLocks[gopath].Unlock()
}

func rlock(gopath string) {
	if _, ok := gopathLocks[gopath]; !ok {
		gopathLocks[gopath] = new(sync.RWMutex)
	}
	gopathLocks[gopath].RLock()
}

func runlock(gopath string) {
	gopathLocks[gopath].RUnlock()
}

// CaddyPlugin holds information about a Caddy plugin to build.
type CaddyPlugin struct {
	Package string `json:"package"` // fully qualified package import path
	Version string `json:"version"` // commit, tag, or branch to checkout
	Repo    string `json:"repo"`    // git clone URL -- TODO: used?
	Name    string `json:"-"`       // name of plugin: not used here, but used by devportal
	ID      string `json:"-"`       // ID of plugin: not used here, but used by devportal
}

// BuildConfig holds information to conduct a build of some
// version of Caddy and a number of plugins.
type BuildConfig struct {
	CaddyVersion string        `json:"caddy_version"`
	Plugins      []CaddyPlugin `json:"plugins"`
}

const ldFlagVarPkg = "github.com/mholt/caddy/caddy/caddymain"

// makeLdFlags makes a string to pass in as ldflags when building Caddy.
// This automates proper versioning, so it uses git to get information
// about the current version of Caddy.
func makeLdFlags(repoPath string) (string, error) {
	run := func(cmd *exec.Cmd, ignoreError bool) (string, error) {
		cmd.Dir = repoPath
		out, err := cmd.Output()
		if err != nil && !ignoreError {
			return string(out), err
		}
		return strings.TrimSpace(string(out)), nil
	}

	var ldflags []string

	for _, ldvar := range []struct {
		name  string
		value func() (string, error)
	}{
		// Timestamp of build
		{
			name: "buildDate",
			value: func() (string, error) {
				return time.Now().UTC().Format("Mon Jan 02 15:04:05 MST 2006"), nil
			},
		},

		// Current tag, if HEAD is on a tag
		{
			name: "gitTag",
			value: func() (string, error) {
				// OK to ignore error since HEAD may not be at a tag
				return run(exec.Command("git", "describe", "--exact-match", "HEAD"), true)
			},
		},

		// Nearest tag on branch
		{
			name: "gitNearestTag",
			value: func() (string, error) {
				return run(exec.Command("git", "describe", "--abbrev=0", "--tags", "HEAD"), false)
			},
		},

		// Commit SHA
		{
			name: "gitCommit",
			value: func() (string, error) {
				return run(exec.Command("git", "rev-parse", "--short", "HEAD"), false)
			},
		},

		// Summary of uncommitted changes
		{
			name: "gitShortStat",
			value: func() (string, error) {
				return run(exec.Command("git", "diff-index", "--shortstat", "HEAD"), false)
			},
		},

		// List of modified files
		{
			name: "gitFilesModified",
			value: func() (string, error) {
				return run(exec.Command("git", "diff-index", "--name-only", "HEAD"), false)
			},
		},
	} {
		value, err := ldvar.value()
		if err != nil {
			return "", err
		}
		ldflags = append(ldflags, fmt.Sprintf(`-X "%s.%s=%s"`, ldFlagVarPkg, ldvar.name, value))
	}

	return strings.Join(ldflags, " "), nil
}

// dirExists returns true if dir exists and is a
// directory, or false in any other case.
func dirExists(dir string) bool {
	info, err := os.Stat(dir)
	if err != nil {
		return !os.IsNotExist(err)
	}
	return info.IsDir()
}

// deepCopyConfig configures a deep copy.
type deepCopyConfig struct {
	Source        string // source folder
	Dest          string // destination folder
	SkipHidden    bool   // skip hidden files (files or folders starting with ".")
	SkipSymLinks  bool   // skip symbolic links
	SkipTestFiles bool   // skips *_test.go files and testdata folders - TODO: doesn't generalize well; maybe a SkipFn instead?
	PreserveOwner bool   // preserve file/folder ownership
}

// deepCopy makes a deep copy according to cfg, overwriting any existing files.
// cfg.Source and cfg.Dest are required. File and folder permissions are always
// preserved. If an error is returned, not all files were copied successfully.
// This function blocks.
func deepCopy(cfg deepCopyConfig) error {
	if cfg.Source == "" || cfg.Dest == "" {
		return fmt.Errorf("no source or no destination; both required")
	}

	setOwner := func(srcInfo os.FileInfo, destPath string) error {
		if cfg.PreserveOwner {
			statT := srcInfo.Sys().(*syscall.Stat_t)
			err := os.Chown(destPath, int(statT.Uid), int(statT.Gid))
			if err != nil {
				return fmt.Errorf("chown (preserving) destination file: %v", err)
			}
			return nil
		} else {
			return chown(destPath)
		}
	}

	// prewalk: start by making destination directory
	// (can't skip this by using MkdirAll in Walk
	// because Chown would only change the leaf
	// directory, not any parents it created; we
	// must do each dir individually - however,
	// this only applies if we're trying to change
	// the owner as if that user did the copy)
	srcInfo, err := os.Stat(cfg.Source)
	if err != nil {
		return err
	}
	destComponents := strings.Split(cfg.Dest, string(filepath.Separator))
	if len(destComponents) > 0 && destComponents[0] == "" {
		destComponents[0] = "/"
	}
	for i := range destComponents {
		destSoFar := filepath.Join(destComponents[:i+1]...)
		_, err := os.Stat(destSoFar)
		if os.IsNotExist(err) {
			err = os.Mkdir(destSoFar, srcInfo.Mode()&os.ModePerm)
			if err != nil {
				return err
			}
			err = setOwner(srcInfo, destSoFar)
			if err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
	}

	// now traverse the source directory and copy each file
	return filepath.Walk(cfg.Source, func(path string, info os.FileInfo, err error) error {
		// error accessing current file
		if err != nil {
			return err
		}

		// skip files/folders without a name
		if info.Name() == "" {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		// skip symlinks, if requested
		if cfg.SkipSymLinks && (info.Mode()&os.ModeSymlink > 0) {
			return nil
		}

		// skip hidden folders, if requested
		if cfg.SkipHidden && info.Name()[0] == '.' {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		// skip testdata folders and _test.go files, if requested
		if cfg.SkipTestFiles {
			if info.IsDir() && info.Name() == "testdata" {
				return filepath.SkipDir
			}
			if !info.IsDir() && strings.HasSuffix(info.Name(), "_test.go") {
				return nil
			}
		}

		// if directory, create destination directory (if not
		// already created by our pre-walk)
		if info.IsDir() {
			subdir := strings.TrimPrefix(path, cfg.Source)
			destDir := filepath.Join(cfg.Dest, subdir)
			if _, err := os.Stat(destDir); os.IsNotExist(err) {
				err := os.Mkdir(destDir, info.Mode()&os.ModePerm)
				if err != nil {
					return err
				}
			}
			return setOwner(info, destDir)
		}

		// open source file
		fsrc, err := os.Open(path)
		if err != nil {
			return err
		}

		// create destination file
		destPath := filepath.Join(cfg.Dest, strings.TrimPrefix(path, cfg.Source))
		fdest, err := os.OpenFile(destPath, os.O_RDWR|os.O_CREATE|os.O_TRUNC, info.Mode()&os.ModePerm)
		if err != nil {
			fsrc.Close()
			if _, err := os.Stat(destPath); err == nil {
				return fmt.Errorf("opening destination (which already exists): %v", err)
			}
			return err
		}

		// set ownership of file
		err = setOwner(info, destPath)
		if err != nil {
			return fmt.Errorf("chown destination file: %v", err)
		}

		// copy the file and ensure it gets flushed to disk
		if _, err = io.Copy(fdest, fsrc); err != nil {
			fsrc.Close()
			fdest.Close()
			return err
		}
		if err = fdest.Sync(); err != nil {
			fsrc.Close()
			fdest.Close()
			return err
		}

		// close both files
		if err = fsrc.Close(); err != nil {
			fdest.Close()
			return err
		}
		if err = fdest.Close(); err != nil {
			return err
		}

		return nil
	})
}

// DeployRequest represents a request to test an updated
// version of a plugin against a specific Caddy version.
type DeployRequest struct {
	// The version of Caddy into which to plug in.
	CaddyVersion string `json:"caddy_version"`

	// The import (package) path of the plugin, and its version.
	PluginPackage string `json:"plugin_package"`
	PluginVersion string `json:"plugin_version"`

	// The list of platforms on which the plugin(s) must
	// build successfully.
	RequiredPlatforms []Platform `json:"required_platforms"`
}

// BuildRequest is a request for a build of Caddy.
type BuildRequest struct {
	Platform
	BuildConfig
}

// Serialize returns a deterministic string representation of this
// build request. Like a hash, but reversible. It's designed to be
// easy-ish to read and conveniently sortable. This function DOES
// reorder the contents of br.BuildConfig.Plugins so they are in
// lexicographical order. This string does NOT account for plugin
// versions, sorry. Also, it uses plugin name instead of import
// path for space efficiency, even though technically import path
// might be slightly more accurate/stable. Plugin names must be
// standardized as to case (lowercase).
func (br BuildRequest) Serialize() string {
	sort.Slice(br.BuildConfig.Plugins, func(i, j int) bool {
		return br.BuildConfig.Plugins[i].Name < br.BuildConfig.Plugins[j].Name
	})
	var plugins string
	for _, plugin := range br.BuildConfig.Plugins {
		plugins += plugin.Name + ","
	}
	if len(plugins) > 0 {
		plugins = plugins[:len(plugins)-1]
	}
	return fmt.Sprintf("%s:%s.%s.%s:%s", br.BuildConfig.CaddyVersion,
		br.Platform.OS, br.Platform.Arch, br.Platform.ARM, plugins)
}

// Sign signs the file using the configured PGP private key
// and returns the ASCII-armored bytes, or an error.
func Sign(file *os.File) (*bytes.Buffer, error) {
	if Signer == nil {
		return nil, fmt.Errorf("no signing key loaded")
	}
	buf := new(bytes.Buffer)
	err := openpgp.ArmoredDetachSign(buf, Signer, file, nil)
	if err != nil {
		return nil, fmt.Errorf("signing error: %v", err)
	}
	return buf, nil
}
