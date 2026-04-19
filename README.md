# auto-deploy

A lightweight Go service that automates binary deployments for multiple applications.
When GitHub Actions publishes a release, it triggers this service to download the new binary,
back up the old one, replace it, and restart the systemd service.

## How It Works

GitHub Release → GitHub Actions (curl POST) → Webhook Service (:9000)

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
- **Access**: `http://YOUR_SERVER:9000/admin/apps`
- **Authentication**: Protected by HTTP Basic Auth using `ADMIN_USERNAME` and `ADMIN_PASSWORD`.
- **Features**:
  - Add, edit, enable, or disable applications.
  - View deployment history and status for each app.
  - Manually retry failed or successful deployments.

## Prerequisites

- Go 1.21+ (for building)
- Linux with systemd
- Network access to `github.com` from the server
- Root access (for binary replacement and `systemctl` commands)

## Build

```bash
# Build for current architecture
make build

# Cross-compile for Armbian (ARM64)
make build-arm64
```

## Server Setup

1. **Copy binary to server**
   ```bash
   scp auto-deploy-arm64 root@YOUR_SERVER:/opt/auto-deploy/auto-deploy
   ```

2. **Set up environment**
   ```bash
   cp auto-deploy.env.example /etc/auto-deploy.env
   # Edit /etc/auto-deploy.env — ADMIN_PASSWORD is required
   nano /etc/auto-deploy.env
   ```

3. **Install systemd service**
   ```bash
   cp auto-deploy.service /etc/systemd/system/
   systemctl daemon-reload
   systemctl enable --now auto-deploy.service
   ```

4. **Verify it's running**
   ```bash
   systemctl status auto-deploy.service
   ```

## GitHub Setup

Each application configured in the Admin UI has a unique webhook secret.
1. Create your application in the Admin UI.
2. Add the following secrets to your GitHub repository (Settings → Secrets and variables → Actions):
   - `DEPLOY_WEBHOOK_SECRET`: The secret token generated for the app.
   - `DEPLOY_WEBHOOK_URL`: `http://YOUR_SERVER_IP:9000`

Refer to `deploy-step.yml` for the workflow snippet to include in your GitHub Actions.

## Environment Variables

### Service Settings
| Variable | Default | Required | Description |
|----------|---------|----------|-------------|
| `LISTEN_ADDR` | `:9000` | No | Address to listen on |
| `DEPLOY_QUEUE_DB_PATH`| `deploy-queue.db` | No | SQLite file for registry and queue |
| `DEPLOY_QUEUE_MAX` | `10` | No | Max pending deploys per app |
| `ADMIN_USERNAME` | `admin` | No | Admin UI username |
| `ADMIN_PASSWORD` | — | ✅ Yes | Admin UI password |

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
