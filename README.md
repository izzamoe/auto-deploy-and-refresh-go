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
- **Access**: `http://YOUR_SERVER:9000/admin/login`
- **Authentication**: Login form issues a signed JWT stored as an httpOnly cookie (24h TTL). Set `ADMIN_USERNAME` and `ADMIN_PASSWORD` in your env file.
- **Features**:
  - Add, edit, enable, or disable applications.
  - View deployment history and status for each app.
  - Manually retry failed or successful deployments.
  - Live deployment progress via WebSocket.

## Prerequisites

- Linux with systemd
- Supported architectures: `amd64` (x86_64), `arm64` (aarch64)
- Network access to `github.com` from the server
- Root access (required for `install.sh` and service management)
- Go 1.21+ (only if building from source)

## Build

```bash
# Build for current architecture
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

The installer requires `root` privileges via `sudo`. It automatically detects your architecture, downloads the correct release, and configures systemd.

```bash
# Install the latest stable release
curl -fsSL https://raw.githubusercontent.com/izzamoe/auto-deploy-and-refresh-go/master/install.sh | sudo sh

# Install a specific tagged version
curl -fsSL https://raw.githubusercontent.com/izzamoe/auto-deploy-and-refresh-go/master/install.sh | sudo sh -s -- v1.2.3
```

On upgrade, the installer overwrites the binary and service unit but preserves your existing `/etc/auto-deploy.env` configuration.

## Server Setup

The recommended way to set up the server is using the installer.

1. **Run the installer**
   ```bash
   curl -fsSL https://raw.githubusercontent.com/izzamoe/auto-deploy-and-refresh-go/master/install.sh | sudo sh
   ```

2. **Configure the environment**
   The installer creates a default `/etc/auto-deploy.env`. Edit it to set your credentials:
   ```bash
   # ADMIN_PASSWORD defaults to "hehe" if not set — change it in production
   sudo nano /etc/auto-deploy.env
   ```

3. **Restart the service**
   ```bash
   sudo systemctl restart auto-deploy.service
   ```

4. **Verify status**
   ```bash
   systemctl status auto-deploy.service
   ```

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
| `ADMIN_USERNAME` | `admin` | No | Admin UI username |
| `ADMIN_PASSWORD` | `hehe` | No | Admin UI password — change in production |

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
# Run automated tests
make test

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

# Run the deterministic admin smoke tests
npm run pw:smoke

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
