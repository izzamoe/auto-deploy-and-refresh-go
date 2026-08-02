# Read this first

> You are most likely an AI agent working in an application repository whose owner deploys through this auto-deploy instance. Your job is to produce a CI/CD pipeline this server will accept — this document states exactly what it requires.

auto-deploy runs on the owner's Linux host and manages their app as a systemd service. It
does **not** build anything. Your pipeline builds, publishes a GitHub Release, and then
tells this server which tag to roll out. The server does the rest.

```
your CI:  build → publish GitHub Release → POST /webhook {"tag": "..."}
                                                 │
this server:  download asset → validate ELF → backup → swap → systemctl restart
              → is-active check ×5 → rollback on failure
```

## Before you write any YAML

Five values are needed, and **four you cannot guess** — they are configured per
application in this server's admin UI. Ask the repository owner, or have them read the
values off the app's edit page.

| Value | Where it comes from | Why you cannot guess it |
| --- | --- | --- |
| Artifact name | app config | The release asset filename must match it **byte for byte**. |
| GitHub repo | app config | Must be the repo the release is published to. |
| Deploy token | app config | Per-application bearer token. |
| Webhook URL | this server's public URL + `/webhook` | Depends on the deployment. |
| Tag | your pipeline | Whatever tag you publish the release under. |

Getting the artifact name wrong is the most common failure: the server builds the download
URL by literal string concatenation, so a near-miss is a 404, never a fuzzy match.

The target architecture is **not** on that list — it is reported in *This host* at the top
of the index, read from the running server process. Use it directly.

## What the server guarantees

- Deploys are queued, not synchronous. The webhook returns before anything is downloaded.
- One deploy at a time per application; different applications proceed in parallel.
- The previous binary is kept as `<binary>.bak` and restored automatically when the new
  one fails its health check.
- The binary is `chmod 0755` by the server — your pipeline does not need to set the
  executable bit on the release asset.
- The target directory is created if missing, so a first-ever deploy works.

## What it does not do

- No building, no containers, no remote hosts — the target is a systemd unit on this same
  machine.
- No multi-arch selection. One application maps to exactly one artifact name, so the binary
  published under that name must match the host's architecture.
- No secrets delivery. Application environment variables are managed in the admin UI, not
  through your pipeline.
