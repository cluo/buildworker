Build Worker
============

[![Documentation](https://img.shields.io/badge/godoc-reference-blue.svg?style=flat-square)](https://godoc.org/github.com/caddyserver/buildworker)

Build Worker is the Caddy build server. It maintains a cache of source code repositories in a local GOPATH so that it can build Caddy with desired plugins on demand.

## Installation

NOTE: You do _not_ need this program unless you wish to use the build features of the Caddy website on your own machine.

```bash
$ go get github.com/caddyserver/buildworker/cmd/buildworker
```


## Basic Use

```
$ buildworker
```

This will start a build worker listening on localhost. With this and the [devportal](https://github.com/caddyserver/devportal) running, you can use the [website](https://github.com/caddyserver/website)'s download and build features on your own computer. A log will be written to buildworker.log.

Run `buildworker -h` to see a list of flags/options.

## Explanation

While the `buildworker` package can be used by other programs that wish to build Caddy in dependable ways, the `main` package here adds HTTP handlers so that the Caddy website can request jobs. The build server's handlers are not directly exposed to the Internet, and all requests must be authenticated.

The build server is entirely stateless. It will rebuild its GOPATH (considered merely a cache) if necessary.

Build Worker will assume the GOPATH environment variable is the absolute path to the _master_ GOPATH, which is the GOPATH that will be maintained as new releases are made and from which new builds will be produced. As part of this maintenance, Build Worker will get dependencies with `go get` (for builds) and `go get -u` (for deploys) in `$GOPATH`. If you are not comfortable running these commands in your GOPATH, set the GOPATH environment variable to something else for Build Worker. It will be created from scratch if it does not exist.

When creating a build or running checks to do a new release/deploy, Build Worker creates a temporary directory as a separate GOPATH, copies the requested packages (plugins) into it from the master GOPATH (including the Caddy core packages, of course), and does `git checkout` in that temporary workspace before running tests or builds. This ensures that the tests and builds are using the versions of Caddy and plugins that are desired.

Remember to set the `GOPATH` environment variable to something else if you don't want to run updates in your working GOPATH.

The build worker is optimized for fast, on-demand builds. Deploys (a.k.a. releases) can take a little longer, even several minutes.

The command of this repository is the production build server, and the library is also used by the [Caddy releaser](https://github.com/caddyserver/releaser) tool. The [Caddy developer portal](https://github.com/caddyserver/devportal), which is the backend to the Caddy website, makes requests to this build server.

## Authenticated Requests

A build worker is not exposed directly to the Internet. Credentials are set to authenticate requests from the dev portal:

```bash
$ BUILDWORKER_CLIENT_ID=username BUILDWORKER_CLIENT_KEY=password buildworker
```

Replace the credentials with your own secret values. This will start buildworker listening on 127.0.0.1:2017 (you can change the address with the `-addr` option). All requests to buildworker must be authenticated using HTTP Basic Auth with the credentials you've specified.

## Signed Builds

The build worker can sign the archives it creates. An archive is a .zip or .tar.gz file&mdash;depending on platform&mdash;that contains the caddy binary and other distribution files. A "build" of Caddy includes an archive and its authenticated signature so users can be sure it comes from a genuine Caddy build server and has not been modified.

The `buildworker` command will automatically try to load the OpenPGP private key in `signing_key.asc` and decrypt it with the password in `signing_key_password.txt` so that builds can be signed. You can change these file paths with the `SIGNING_KEY_FILE` and `KEY_PASSWORD_FILE` environment variables, respectively. The key 

## Privileges and Jailing

By specifying the `-uid` and `-chroot` command line options, the build worker will:

- run all commands as the user with the given uid (and same gid),
- run all commands in a jailed (chroot'ed) environment,
- and chown all files needed by the commands to uid:uid.

The build worker will not make any privilege modifications if these flags are absent. These flags work only Linux, BSD, and macOS systems. Using these flags requires great care to set up the machine properly.

Note: if the build worker runs without -chroot and/or without -uid, and then is run later with either one or both of those options (or vice versa), there may be permissions errors when running commands. This is because the commands will be run as a different user or in a jailed file system compared to before, and some or all needed files may be owned by a different user, and thus possibly inaccessible to the other one. If switching use of these flags, clear the master GOPATH first.

All `go` commands will _not_ inherit the parent build worker's environment (with exceptions of GOPATH, PATH, and TMPDIR).

All the above security measures are used on the production Caddy build workers.

## HTTP Endpoints

### GET /supported-platforms

Get a list of platforms supported for building.

**Example:**

```bash
curl --request GET \
  --url http://localhost:2017/supported-platforms \
  --header 'authorization: Basic ZGV2OmhhcHB5cGFzczEyMw=='
```

### POST /deploy-caddy

Invoke a deploy of Caddy.

**Example:**

```bash
curl --request POST \
  --url http://localhost:2017/deploy-caddy \
  --header 'authorization: Basic ZGV2OmhhcHB5cGFzczEyMw==' \
  --header 'content-type: application/json' \
  --data '{"caddy_version": "master"}'
```

### POST /deploy-plugin

Invoke a deploy of a Caddy plugin.

**Example:**

```bash
curl --request POST \
  --url http://localhost:2017/deploy-plugin \
  --header 'authorization: Basic ZGV2OmhhcHB5cGFzczEyMw==' \
  --header 'content-type: application/json' \
  --data '{
	"caddy_version": "v0.9.4",
	"plugin_package": "github.com/xuqingfeng/caddy-rate-limit",
	"plugin_version": "v1.2"
}'
```


### POST /build

Produce a build of Caddy, optionally with plugins.

**Example:**

```bash
curl --request POST \
  --url http://localhost:2017/build \
  --header 'authorization: Basic ZGV2OmhhcHB5cGFzczEyMw==' \
  --header 'content-type: application/json' \
  --data '{
	"caddy_version": "v0.9.4",
	"GOOS": "darwin",
	"GOARCH": "amd64",
	"plugins": [
		{
			"package": "github.com/xuqingfeng/caddy-rate-limit",
			"version": "164fb914fa8c8d7c9e8d59290cdb0831ace2daef"
		},
		{
			"package": "github.com/abiosoft/caddy-git",
			"version": "v1.3"
		}
	]
}'
```
