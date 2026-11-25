# Server configuration (operator reference)

> Not needed to write a pipeline. This is for whoever runs the auto-deploy instance itself: process-level settings come from environment variables, per-application settings live in SQLite and are edited through the admin UI.

## Environment variables

| Variable | Default | Purpose |
| --- | --- | --- |
| `LISTEN_ADDR` | `:9000` | HTTP listen address. |
| `DEPLOY_QUEUE_DB_PATH` | `deploy-queue.db` | SQLite file holding all persistent state. |
| `DEPLOY_QUEUE_MAX` | `10` | Max pending deploys per application; must be a positive integer or startup fails. |
| `DOWNLOAD_DNS` | `1.1.1.1` | DNS resolver used for release downloads. |
| `SESSION_KEY_PATH` | `session.key` next to the database | Signing key of the admin sessions; created with mode 0600 on first start. Deleting it signs everyone out. |
| `COOKIE_SECURE` | `true` | `Secure` flag and `__Host-` name of the session cookie. Serving the admin over plain HTTP requires `COOKIE_SECURE=false`, otherwise the browser drops the cookie and login silently fails. |
| `TRUSTED_PROXY_CIDRS` | empty | Comma-separated CIDRs/IPs of the reverse proxy whose `CF-Connecting-IP`/`X-Forwarded-For` are trusted as the client address. **Required behind Cloudflare, `cloudflared` or any proxy** (see below). |
| `GITHUB_TOKEN` | empty | Raises the GitHub API rate limit and enables private-repository artifact downloads. Can also be stored in the database from the admin UI. |
| `SELF_SERVICE_NAME` | `auto-deploy.service` | This service's own systemd unit, read by the admin log viewer. |

## Bootstrap-only variables

`WEBHOOK_SECRET`, `DEPLOY_BINARY_PATH`, `DEPLOY_SERVICE_NAME`, `GITHUB_REPO`, and
`ARTIFACT_NAME` seed a single application on first startup when the registry is empty.
Once any application exists they are ignored — manage applications in the admin UI at `/admin/`.

## Per-application settings

Held in SQLite, not in the environment, and edited on the app's page in the admin UI:
target binary path, systemd unit name, GitHub repository, artifact name, enabled flag, the
token hash, per-app environment variables injected into the deployed unit, and per-app
command-line arguments appended to that unit's `ExecStart` line after the binary path.

Environment variables and arguments are written into the unit file, so they only reach the
service once the operator regenerates and applies the service unit and restarts it — a
deploy on its own does not pick up a change to either.

These are the values a pipeline author must be told; see the table in *Read this first*.

## Admin accounts and sessions

There are no admin credentials in the environment. The first account of a fresh database
(`admin`) gets a random password that is written to `initial-admin-password` next to the
database (mode 0600, never logged); the owner reads it, signs in at `/admin/` and must
change it, which deletes the file. The retired username/password variables of earlier
releases are ignored (a start-up warning names them).

Signing in (`POST /admin/api/v1/session`, from the sign-in page) issues a server-side
session in an httpOnly cookie: idle timeout 12 hours, absolute limit 7 days. Every route
marked *admin session* in the endpoint table requires that cookie or an admin API token
(`Authorization: Bearer adt_...`, created on the account page, shown once; tokens cannot
manage accounts or tokens). Scripts that speak HTTP Basic keep working with the API token
as the password (the user name is ignored); the account password is never accepted as a
Basic credential. Deploy (webhook) tokens do not work on admin routes, and no
admin credential is needed for `/webhook`. Sign-in attempts are rate limited (5 per 15
minutes per address and user, 20 per minute per address).

## Behind Cloudflare, cloudflared or a reverse proxy

Rate limits and the audit log use the client address. Behind a proxy the socket peer is
the proxy, so the real address is read from `CF-Connecting-IP` / `X-Forwarded-For` **only
when the peer is inside `TRUSTED_PROXY_CIDRS`**. Setting it is a required step:

- `cloudflared` on the same host: `TRUSTED_PROXY_CIDRS=127.0.0.1/32`.
- Cloudflare connecting directly: the published Cloudflare ranges, comma separated.
- Another reverse proxy: that proxy's address or network.

If it is empty while a request carries one of those headers, the headers are ignored and
one warning is logged; every visitor then shares the proxy's single rate-limit bucket.
