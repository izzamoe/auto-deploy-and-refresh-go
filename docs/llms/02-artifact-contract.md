# Artifact contract — the part that breaks pipelines

> The release asset must be a raw, uncompressed Linux ELF executable, published under a filename that matches the app's configured artifact name exactly, and no larger than 100 MiB.

Every rule here is enforced in code. Violating any of them fails the deploy, not the
webhook — your pipeline will report success while the rollout dies.

## 1. Exact filename, no pattern matching

The download URL is built by string concatenation:

```
https://github.com/{github_repo}/releases/download/{tag}/{artifact_name}
```

There is no glob, no prefix match, and no "closest asset" fallback. If the app is
configured with artifact name `kas-linux-arm64`, then `kas-linux-arm64-v1.2.3` and
`kas-linux-arm64.tar.gz` both resolve to a 404.

**Consequence for your pipeline:** do not put the version, the commit SHA, or a build
timestamp in the asset filename. Name the output file the exact configured string and let
the tag carry the version.

## 2. Raw ELF binary — never an archive

After download, the first four bytes are checked for the ELF magic `0x7F 'E' 'L' 'F'`.
A `.zip`, `.tar.gz`, `.deb`, or a container image fails validation with
`not an ELF executable`, and the running service is left untouched.

**Consequence:** upload the compiled binary directly as a release asset. Do not compress
it. This is the single most common mistake when adapting a workflow that was written for
a different deploy target.

## 3. Correct GOOS and GOARCH

This host is **`{{GOOS}}/{{GOARCH}}`** — see *This host* in the index for the exact build
command. You do not need to ask anyone; the value is read from the running server.

A macOS or Windows build is not an ELF file and fails rule 2. A Linux build for the wrong
architecture is more dangerous: it passes ELF validation, gets installed, and only then
fails to execute, so the health check times out and the deploy is rolled back. That
surfaces as a mysterious "rolled back" rather than a build error.

```bash
GOOS={{GOOS}} GOARCH={{GOARCH}} CGO_ENABLED=0 go build -trimpath -o <ARTIFACT_NAME> ./cmd/app
```

`CGO_ENABLED=0` matters here too: a cgo build links against shared libraries that may be
absent on the host, which fails at startup in exactly the same confusing way.

## 4. Size ceiling of 100 MiB

Checked against the `Content-Length` of the download before any bytes are written, so an
oversized asset fails immediately. Strip debug symbols if you are near the limit:

```bash
go build -ldflags "-s -w" -o <artifact-name> ./cmd/app
```

## 5. Private repositories

Assets in a private repo are fetched through the GitHub API with a token that the server
holds — configured by the owner under the admin UI's GitHub page, or via `GITHUB_TOKEN`
in the server's environment. Nothing is required from your pipeline, but if the repo is
private and the owner has not configured a token, the download fails with a 404. Tell the
owner to set it rather than trying to work around it in CI.

## Quick self-check

Before publishing, the asset should satisfy:

```bash
file "$ASSET"        # → ELF 64-bit LSB executable, {{ARCH_UNAME}}
stat -c%s "$ASSET"   # → below 104857600
[ "$(basename "$ASSET")" = "$CONFIGURED_ARTIFACT_NAME" ]
```
