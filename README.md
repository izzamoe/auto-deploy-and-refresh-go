# auto-deploy

A lightweight Go service that automates binary deployments for multiple applications.
When a release workflow or manual operator calls the webhook, this service downloads the new binary,
backs up the old one, replaces it, and restarts the systemd service.

## How It Works

GitHub Release → Optional/manual webhook call → Webhook Service (:9000)

1. **Webhook**: The service receives a `POST /webhook` with an app-specific Bearer token.
2. **Resolution**: It resolves the application by the SHA256 hash of the token in its SQLite registry.
3. **Queue**: If valid, the deployment is enqueued. The webhook returns `202 Accepted` immediately.
4. **Coordination**: A background coordinator runs one deployment at a time per application.
5. **Deployment**:
   → Downloads the binary from GitHub Releases
   → Validates the binary (ELF magic bytes)
   → Backs up the current binary (.bak)
   → Replaces the binary (atomically)
   → Restarts the systemd service
   → Performs a health check (5 retries × 2s)
   → Automatically rolls back if the health check fails

## Admin UI

Manage your applications via the built-in Admin UI.
- **Access**: `http://YOUR_SERVER:9000/admin/` (the sign-in page opens by itself). The UI is a
  static Next.js export embedded in the binary (`go:embed`): there is no Node.js on the server.
  It is available in Indonesian and English, with a light and a dark theme.
- **Authentication**: the sign-in page issues a server-side session carried by an httpOnly cookie
  (idle timeout 12 hours, absolute limit 7 days; signing key in `session.key` next to the database,
  see `SESSION_KEY_PATH`). The first account of a fresh database gets a **random password** that is
  written to `initial-admin-password` next to the database (mode `0600`, never logged); you must change it at the
  first sign-in and the file is deleted afterwards. The account page changes the password or the
  username and revokes other sessions.
- **Scripts**: create an **API token** (`adt_...`, shown once, afterwards only its prefix) on the
  account page and send it as `Authorization: Bearer adt_...` to `/admin/api/v1/...`. Scripts that
  already speak HTTP Basic keep working with the **token as the password** (the user name is ignored);
  the account password itself is never accepted as a Basic credential. Tokens cannot manage accounts or other tokens.
- **HTTPS**: the session cookie carries the `Secure` flag by default (`COOKIE_SECURE=true`). Serving the
  admin over plain HTTP (local development) requires `COOKIE_SECURE=false` explicitly.
- **Behind a reverse proxy / Cloudflare**: see [Reverse proxy and client IP](#reverse-proxy-and-client-ip).
- **Features**:
  - Add, edit, enable, or disable applications.
  - Configure how each service is started: per-app **environment variables** and per-app
    **command-line arguments** (flags), both injected into the generated systemd unit —
    the arguments are appended to `ExecStart` after the binary path. Apply the service
    unit and restart the service for either to take effect.
  - View deployment history and status for each app.
  - Manually retry failed or successful deployments, cancel a running one.
  - Live deployment progress and notifications over the events WebSocket (`/admin/events/v1/ws`).
  - JSON API under `/admin/api/v1` (contract: `api/admin-api.v1.openapi.yaml`).

## Machine-Readable Context (`llms.txt`)

Every running instance publishes its own context for AI agents, following the
[llms.txt](https://llmstxt.org/) convention used by sites like
`developers.cloudflare.com/llms.txt`.

The intended reader is an agent working in **a deployed application's repository**, not in
this one: it fetches `https://your-instance/llms.txt` and learns what a pipeline must
produce for this server to accept it — the artifact contract, the webhook call, the
ordering rule, and the failure modes. That removes the need to explain auto-deploy to a
coding assistant by hand every time.

- `GET /llms.txt` — index: the contract, a **This host** block, the section list, and the
  endpoints reachable from CI. Admin routes are deliberately excluded so the one endpoint
  a pipeline needs is not buried.
- `GET /llms-full.txt` — every curated document inlined, plus the complete route table
  for operators.

The *This host* block reports the instance's own `GOOS`/`GOARCH` (plus the `uname -m`
spelling and the Rust target triple), resolved at startup from `runtime.GOARCH`. Since
every managed application is deployed to that same machine, an agent can read the target
architecture instead of asking for it — the build command in the served document already
has it filled in. It is not hardware probing: the server process is itself an ELF binary
the host kernel agreed to run.

Both are public (no session cookie) — an agent cannot fetch a document it must first
authenticate for. They describe the API contract and the host platform only; no
per-application data (artifact name, repo, tokens, deploy history) is included, so an
agent still has to be told those three values by the app's owner.

The files are generated, not hand-written:

| Input | Becomes |
| --- | --- |
| `docs/llms/*.md` | the prose sections, ordered by filename prefix |
| the Go sources (`go/ast` scan of Hertz route registrations) | the endpoint table |

```bash
make llms        # regenerate internal/llmstxt/llms.txt and llms-full.txt, then commit
make llms-check  # CI gate: fails if the committed files are stale
```

`make llms-check` runs in both `ci.yml` and `release.yml`, so a new route or an edited
document cannot ship without the published context being refreshed. The build stamps the
version via `-ldflags`, so the served file reports the exact release it came from.

## Prerequisites

- Linux with systemd
- Supported architectures: `amd64` (x86_64), `arm64` (aarch64)
- Network access to `github.com` from the server
- Root access (required for `install.sh` and service management)
- Go 1.27.4+ and Node.js 26 LTS (only if building from source; release binaries already embed the admin UI and need no Node.js)

## Build

```bash
# Build for current architecture (runs `npm ci` + the Next.js export, embeds it, then `go build`)
make build

# Cross-compile for Armbian (ARM64)
make build-arm64
```

## Release And Installer Contract

The repository contract for distributable releases is:

- Release tags that installers and automation consume must match `v*`.
- Linux release assets are exactly `auto-deploy_linux_amd64.zip` and `auto-deploy_linux_arm64.zip`.
- Each release zip must contain the `auto-deploy` binary only. Runtime files are not bundled into the archive.
- `install.sh` defaults to the latest stable GitHub release when no version is provided.
- `install.sh vX.Y.Z` is the explicit version override and must reference a matching git tag.
- The installer always places the binary at `/opt/auto-deploy/auto-deploy` and the service unit at `/etc/systemd/system/auto-deploy.service`.
- The installer must run as `root` or through `sudo` because it writes system paths and manages `systemctl`.
- On upgrade, the installer overwrites the binary and service unit, but preserves an existing `/etc/auto-deploy.env`.
- `auto-deploy.service` and `auto-deploy.env.example` are fetched separately from `raw.githubusercontent.com` at the exact same ref/tag as the selected release asset.

The machine-readable contract lives in `release-contract.sh`, and `install.sh` embeds the same defaults so a copied bootstrap script still works by itself.

The CI release workflow uses the same contract in two modes: `workflow_dispatch` runs validation and packaging only, while matching `v*` tag pushes publish the GitHub release assets without notifying the downstream deploy webhook automatically.

## Install Or Upgrade

The installer requires `root` privileges via `sudo`. It automatically detects your architecture, downloads the correct release, **verifies the archive against the release's `SHA256SUMS`**, and configures systemd.

Install from a release tag, not from a branch: download `install.sh` and `SHA256SUMS` of that tag, check the script, then run it.

```bash
TAG=v1.2.3
BASE=https://github.com/izzamoe/auto-deploy-and-refresh-go/releases/download/$TAG
curl -fsSLO "$BASE/install.sh" -O "$BASE/SHA256SUMS"
sha256sum --check --ignore-missing SHA256SUMS      # must print: install.sh: OK
sudo sh install.sh "$TAG"
```

On upgrade, the installer overwrites the binary and service unit but preserves your existing `/etc/auto-deploy.env` configuration **and the existing database** (`/opt/auto-deploy/deploy-queue.db`) — registered apps, accounts, Telegram settings, and job history all survive an upgrade. The upgrade only adds columns and tables, so going back to the previous binary on the upgraded database is safe (see `docs/release-notes.md`).

### Upgrade in place with the `upgrade` subcommand

Once installed, the binary ships an `upgrade` subcommand (alias `update`):

```bash
# Upgrade to the latest stable release
sudo auto-deploy upgrade

# Upgrade (or pin) to a specific tagged version
sudo auto-deploy upgrade v1.2.3
```

It requires `root` and `curl`, resolves a release tag, downloads `install.sh` and `SHA256SUMS` from that release's own assets, verifies `install.sh` against `SHA256SUMS` (the installer then verifies the archive against the same file), preserves `/etc/auto-deploy.env` and the database, then restarts the service. A checksum mismatch aborts before anything is installed. Releases published before `SHA256SUMS` existed need the explicit, loudly warned `AUTO_DEPLOY_ALLOW_UNVERIFIED=1 sh install.sh <tag>`. Read `docs/release-notes.md` before upgrading: the first upgrade to this release signs everyone out once.

## Server Setup

The recommended way to set up the server is using the installer.

1. **Run the installer** from a release tag with the checksum check shown in [Install Or Upgrade](#install-or-upgrade).

2. **Configure the environment**
   The installer creates a default `/etc/auto-deploy.env` (there are no admin credentials in it). Decide how the admin is served:
   ```bash
   # plain HTTP (local network, development): the session cookie is Secure by default
   #   COOKIE_SECURE=false
   # behind Cloudflare / cloudflared / a reverse proxy: REQUIRED, see "Reverse proxy and client IP"
   #   TRUSTED_PROXY_CIDRS=127.0.0.1/32
   sudo nano /etc/auto-deploy.env
   ```

3. **Restart the service**
   ```bash
   sudo systemctl restart auto-deploy.service
   ```

4. **Read the first password and sign in**
   ```bash
   sudo cat /opt/auto-deploy/initial-admin-password   # next to the database; random, 0600
   ```
   Open `http://YOUR_SERVER:9000/admin/`, sign in as the account named in the start log (`admin`) and
   choose a new password when asked. The file disappears after the change. An existing installation keeps its accounts.

5. **Verify status**
   ```bash
   systemctl status auto-deploy.service
   ```

### Reverse proxy and client IP

Sign-in attempts, API tokens, the webhook and the audit log are rate limited and recorded per client IP.
Behind a proxy the socket peer is always the proxy, so the real address is read from `CF-Connecting-IP` /
`X-Forwarded-For` **only when the peer is inside `TRUSTED_PROXY_CIDRS`** (otherwise any client could choose its own
bucket). **Behind Cloudflare, `cloudflared` or any reverse proxy, setting `TRUSTED_PROXY_CIDRS` is a required step:**

- Cloudflare Tunnel (`cloudflared` on the same host): `TRUSTED_PROXY_CIDRS=127.0.0.1/32` (add `::1/128` if it connects over IPv6).
- Cloudflare reaching the server directly: the published Cloudflare ranges (<https://www.cloudflare.com/ips/>), comma separated.
- Another reverse proxy: that proxy's address or network.

If the variable is empty while a request carries `CF-Connecting-IP` or `X-Forwarded-For`, the headers are ignored and the
service logs one warning (`forwarding headers ... are present but TRUSTED_PROXY_CIDRS is empty`); until you set it,
every visitor shares the proxy's single rate-limit bucket. An invalid entry stops the start with an explicit error.

## GitHub Setup

Each application configured in the Admin UI has a unique webhook secret.
1. Create your application in the Admin UI.
2. Add the following secrets to your GitHub repository (Settings → Secrets and variables → Actions):
   - `DEPLOY_WEBHOOK_SECRET`: The secret token generated for the app.
   - `DEPLOY_WEBHOOK_URL`: `http://YOUR_SERVER_IP:9000`

Refer to `deploy-step.yml` for an optional/manual workflow snippet to include in a workflow that should notify this service.
The repository release workflow does not include that webhook call automatically; if you copy the snippet into another workflow, keep it after the GitHub release publish step so deployments only see already-published assets.

## Environment Variables

### Service Settings
| Variable | Default | Required | Description |
|----------|---------|----------|-------------|
| `LISTEN_ADDR` | `:9000` | No | Address to listen on |
| `DEPLOY_QUEUE_DB_PATH`| `deploy-queue.db` | No | SQLite file for registry and queue |
| `DEPLOY_QUEUE_MAX` | `10` | No | Max pending deploys per app |
| `DOWNLOAD_DNS` | `1.1.1.1` | No | DNS server used for downloader requests |
| `SESSION_KEY_PATH` | `session.key` next to the database | No | Signing key of the admin sessions (created `0600` on first start; keep it to stay signed in across restarts) |
| `COOKIE_SECURE` | `true` | No | `Secure` flag and `__Host-` name of the session cookie. Plain-HTTP use must set `false` explicitly |
| `TRUSTED_PROXY_CIDRS` | empty | **Yes behind a proxy** | Comma-separated CIDRs/IPs whose `CF-Connecting-IP`/`X-Forwarded-For` are trusted; see [Reverse proxy and client IP](#reverse-proxy-and-client-ip) |
| `GITHUB_TOKEN` | empty | No | Raises the GitHub API rate limit and enables private-repository downloads (also settable in the admin UI) |

The former admin username/password variables are gone: accounts live in the database (first password in
`initial-admin-password`); setting the old variables has no effect (a start-up warning names them).

### Bootstrap Settings (Legacy)
These variables are only used on the first startup if the application registry is empty.
| Variable | Required | Description |
|----------|----------|-------------|
| `WEBHOOK_SECRET` | No | Initial app bearer token |
| `DEPLOY_BINARY_PATH` | No | Initial app binary path |
| `DEPLOY_SERVICE_NAME` | No | Initial app systemd service |
| `GITHUB_REPO` | No | Initial app owner/repo |
| `ARTIFACT_NAME` | No | Initial app artifact name |

## Testing

```bash
# Run automated tests (Go, with the race detector: `go test -race ./...`; frontend: `make fe-test fe-typecheck`)
make test

# Everything CI runs (vet, race tests, govulncheck, tsc, Vitest, Playwright, llms-check)
make ci

# Validate the full local release flow without publishing
make release-validate-local

# Or run the same workflow pieces manually
make clean release
python3 - <<'PY'
import os, zipfile
from pathlib import Path

release_dir = Path('dist/release')
for asset in sorted([os.environ.get('AUTO_DEPLOY_ASSET_AMD64', 'auto-deploy_linux_amd64.zip'), os.environ.get('AUTO_DEPLOY_ASSET_ARM64', 'auto-deploy_linux_arm64.zip')]):
    with zipfile.ZipFile(release_dir / asset) as archive:
        print(asset, archive.namelist())
PY

# Exercise the installer locally against file:// release fixtures
AUTO_DEPLOY_GITHUB_API_BASE=file:///tmp/fake-release \
AUTO_DEPLOY_GITHUB_DOWNLOAD_BASE=file:///tmp/fake-release \
AUTO_DEPLOY_GITHUB_RAW_BASE=file:///tmp/fake-release \
AUTO_DEPLOY_INSTALL_DIR=/tmp/auto-deploy/opt/auto-deploy \
AUTO_DEPLOY_BINARY_PATH=/tmp/auto-deploy/opt/auto-deploy/auto-deploy \
AUTO_DEPLOY_SERVICE_UNIT_PATH=/tmp/auto-deploy/etc/systemd/system/auto-deploy.service \
AUTO_DEPLOY_ENV_PATH=/tmp/auto-deploy/etc/auto-deploy.env \
AUTO_DEPLOY_SYSTEMD_DIR=/tmp/auto-deploy/systemd \
./install.sh v1.2.3

# Install Playwright browser support for the local smoke harness
npm ci
npm run pw:install

# Run the admin end-to-end tests; they build and start their own server on a scratch database.
# Projects: smoke, routing, session, a11y and visual (visual compares against a LOCAL baseline:
# create it first with `npx playwright test --project=visual --update-snapshots`)
npm run pw:smoke
# or only the gating projects:
npx playwright test --project=smoke --project=routing --project=session --project=a11y

# Test with valid token (replace YOUR_APP_SECRET)
# Expected response: 202 Accepted
curl -s -w '\n%{http_code}' -X POST http://localhost:9000/webhook \
  -H "Authorization: Bearer YOUR_APP_SECRET" \
  -H "Content-Type: application/json" \
  -d '{"tag":"v1.0.0"}'

# Test unauthorized access
# Expected response: 401 Unauthorized
curl -s -w '\n%{http_code}' -X POST http://localhost:9000/webhook \
  -H "Content-Type: application/json" \
  -d '{"tag":"v1.0.0"}'
```

## Rollback

If a deployment fails the health check, the service automatically restores the `.bak` file.
To restore manually:
```bash
systemctl stop your-service.service
cp /path/to/binary.bak /path/to/binary
systemctl start your-service.service
```
