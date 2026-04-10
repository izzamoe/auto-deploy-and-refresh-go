# auto-deploy

A lightweight Go webhook receiver that automates binary deployment on your server.
When GitHub Actions publishes a release, it triggers this service to download the new binary,
back up the old one, replace it, and restart the systemd service.

## How It Works

GitHub Release → GitHub Actions (curl POST) → Webhook Service (:9000)
  → Download binary from GitHub Releases
  → Validate (ELF magic bytes)
  → Backup current binary (.bak)
  → Replace binary (atomic)
  → Restart systemd service
  → Health check (5 retries × 2s)
  → Auto-rollback if health check fails

## Prerequisites

- Go 1.21+ (for building)
- Linux with systemd
- Network access to `github.com` from the server
- Root access (for writing to /root/pb/ and running systemctl)

## Build

```bash
# Build for current architecture
make build

# Cross-compile for Armbian (ARM64)
make build-arm64
```

## Server Setup

```bash
# 1. Copy binary to server
scp auto-deploy-arm64 root@YOUR_SERVER:/opt/auto-deploy/auto-deploy

# 2. Set up environment
cp auto-deploy.env.example /etc/auto-deploy.env
# Edit /etc/auto-deploy.env — set WEBHOOK_SECRET to a strong random token
nano /etc/auto-deploy.env

# 3. Install systemd service
cp auto-deploy.service /etc/systemd/system/
systemctl daemon-reload
systemctl enable --now auto-deploy.service

# 4. Verify it's running
systemctl status auto-deploy.service
```

## GitHub Setup

Add these secrets to your GitHub repository:
  Settings → Secrets and variables → Actions → New repository secret

  DEPLOY_WEBHOOK_SECRET  →  Same value as WEBHOOK_SECRET in /etc/auto-deploy.env
  DEPLOY_WEBHOOK_URL     →  http://YOUR_SERVER_IP:9000

Then copy the step from deploy-step.yml into your existing release workflow:

```yaml
- name: Trigger deployment
  if: success()
  run: |
    HTTP_STATUS=$(curl -s -o /tmp/deploy-response.txt -w '%{http_code}' \
      -X POST "${{ secrets.DEPLOY_WEBHOOK_URL }}/webhook" \
      -H "Authorization: Bearer ${{ secrets.DEPLOY_WEBHOOK_SECRET }}" \
      -H "Content-Type: application/json" \
      -d "{\"tag\": \"${{ github.ref_name }}\"}" \
      --connect-timeout 10 \
      --max-time 120)
    echo "Deploy webhook response (HTTP $HTTP_STATUS):"
    cat /tmp/deploy-response.txt
    echo ""
    if [ "$HTTP_STATUS" -lt 200 ] || [ "$HTTP_STATUS" -ge 300 ]; then
      echo "::error::Deployment failed with HTTP $HTTP_STATUS"
      exit 1
    fi
    echo "Deployment triggered successfully"
```

## Environment Variables

| Variable | Default | Required | Description |
|----------|---------|----------|-------------|
| `WEBHOOK_SECRET` | — | ✅ Yes | Bearer token for auth |
| `DEPLOY_BINARY_PATH` | `/root/pb/pocketbase` | No | Path to binary to replace |
| `DEPLOY_SERVICE_NAME` | `pocketbase.service` | No | Systemd unit to restart |
| `LISTEN_ADDR` | `:9000` | No | Address to listen on |
| `GITHUB_REPO` | `izzamoe/backend-kas` | No | GitHub repo (owner/repo) |
| `ARTIFACT_NAME` | `kas-linux-arm64` | No | Release artifact filename |

## Testing

```bash
# Check service status
systemctl status auto-deploy.service

# Test auth rejection (should return 401)
curl -s -w '\n%{http_code}' -X POST http://localhost:9000/webhook \
  -H 'Content-Type: application/json' \
  -d '{"tag":"v1.0.0"}'

# Test with valid token (replace YOUR_SECRET)
curl -s -w '\n%{http_code}' -X POST http://localhost:9000/webhook \
  -H 'Authorization: Bearer YOUR_SECRET' \
  -H 'Content-Type: application/json' \
  -d '{"tag":"v1.0.0"}'
```

## Rollback

```bash
# If deployment failed and you need to restore manually:
systemctl stop pocketbase.service
cp /root/pb/pocketbase.bak /root/pb/pocketbase
systemctl start pocketbase.service
```
