# Server configuration (operator reference)

> Not needed to write a pipeline. This is for whoever runs the auto-deploy instance itself: process-level settings come from environment variables, per-application settings live in SQLite and are edited through the admin UI.

## Environment variables

| Variable | Default | Purpose |
| --- | --- | --- |
| `LISTEN_ADDR` | `:9000` | HTTP listen address. |
| `DEPLOY_QUEUE_DB_PATH` | `deploy-queue.db` | SQLite file holding all persistent state. |
| `DEPLOY_QUEUE_MAX` | `10` | Max pending deploys per application; must be a positive integer or startup fails. |
| `DOWNLOAD_DNS` | `1.1.1.1` | DNS resolver used for release downloads. |
| `ADMIN_USERNAME` | `admin` | Admin login. Setting it to an empty string is a startup error. |
| `ADMIN_PASSWORD` | `hehe` | Admin password — change it on any reachable install. |
| `COOKIE_SECURE` | unset | Set to `true`/`1` only when serving over HTTPS. Setting it on plain HTTP makes the browser drop the session cookie and login silently fails. |
| `GITHUB_TOKEN` | empty | Raises the GitHub API rate limit and enables private-repository artifact downloads. Can also be stored in the database from the admin UI. |
| `SELF_SERVICE_NAME` | `auto-deploy.service` | This service's own systemd unit, read by the admin log viewer. |

## Bootstrap-only variables

`WEBHOOK_SECRET`, `DEPLOY_BINARY_PATH`, `DEPLOY_SERVICE_NAME`, `GITHUB_REPO`, and
`ARTIFACT_NAME` seed a single application on first startup when the registry is empty.
Once any application exists they are ignored — manage applications at `/admin/apps`.

## Per-application settings

Held in SQLite, not in the environment, and edited on the app's page in the admin UI:
target binary path, systemd unit name, GitHub repository, artifact name, enabled flag, the
token hash, and per-app environment variables injected into the deployed unit.

These are the values a pipeline author must be told; see the table in *Read this first*.

## Admin session

`POST /admin/login` issues a signed JWT in an httpOnly cookie with a 24-hour TTL. Every
route marked *admin session* in the endpoint table requires that cookie. Deploy tokens do
not work on admin routes, and the admin cookie is not needed for `/webhook`.
