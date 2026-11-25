# Release notes (next release)

The first upgrade to this release still goes through the previous binary's mechanism (see
"Upgrading"). Read "What you must do" first.

## What you must do

1. **Sign in again, once.** Every browser session is invalidated by the upgrade (the old
   signing secret was never stored). Nothing else is lost: apps, tokens, Telegram settings
   and history are kept.
2. **Behind Cloudflare, `cloudflared` or a reverse proxy: set `TRUSTED_PROXY_CIDRS`** in
   `/etc/auto-deploy.env` (`127.0.0.1/32` for a tunnel on the same host, or the Cloudflare
   ranges for direct connections), then restart. Without it every visitor shares the
   proxy's single rate-limit bucket and the log carries one warning (`forwarding headers
   ... are present but TRUSTED_PROXY_CIDRS is empty`).
3. **Plain HTTP only: set `COOKIE_SECURE=false`.** The session cookie is `Secure` (and
   `__Host-` prefixed) by default; over plain HTTP the browser drops it and sign-in appears
   to do nothing.
4. Scripts that call `/admin/api/...`: see "Accounts and API access".

## Accounts and API access

- **Accounts live in the database.** The two `ADMIN_*` variables (user name and password)
  are removed and ignored; a start-up warning names them when they are still set. A fresh
  database creates the account `admin` with a random password written to
  `initial-admin-password` next to the database (mode 0600, never logged); sign in, change it
  when asked, and the file is deleted. An existing installation keeps its accounts.
- **API tokens** (`adt_...`) are created on the account page, shown once and afterwards
  only by prefix. Send `Authorization: Bearer adt_...`. Tokens can do everything an
  admin session can except manage accounts and tokens.
- **Basic authentication for scripts is still accepted**, with the API token as the
  password (the user name is ignored). The account password is not accepted as a Basic
  credential any more; a script that used it must switch to a token.
- Sessions: idle timeout 12 hours, absolute limit 7 days, revoked on password change, and
  "sign out other sessions" on the account page. Sign-in is rate limited (5 per 15 minutes
  per address and user, 20 per minute per address).
- The admin API is `/admin/api/v1` (`api/admin-api.v1.openapi.yaml`). The pre-v1 paths
  `/admin/api/*` keep working for this release only and answer with `Deprecation: true`
  and a `Link` to the v1 successor; move scripts now.

## Removed

- The server-rendered admin (`/admin/apps`, `/admin/history`, the old login form and its
  POST routes) is replaced by the Next.js UI embedded in the binary: the old page paths
  (`/admin/apps`, `/admin/history`, `/admin/login`) redirect (308) to the new pages, the
  form POST routes are gone (404).
- The progress WebSocket of the old pages (the route under `/admin/progress/`) is gone (404); live data uses
  `/admin/events/v1/ws`. No consumer existed.

## Upgrading

- `auto-deploy upgrade [vX.Y.Z]` no longer runs a script from the `master` branch. It
  resolves a release tag, downloads `install.sh` and `SHA256SUMS` from that release's own
  assets, verifies `install.sh` against `SHA256SUMS`, and `install.sh` verifies the
  release archive against the same file before unpacking it. A checksum mismatch aborts
  before anything is installed. Releases published before `SHA256SUMS` existed cannot be
  upgraded to with this command; install them with
  `AUTO_DEPLOY_ALLOW_UNVERIFIED=1 sh install.sh <tag>` (loud warning, your call).
- The first upgrade to this release still runs through the OLD binary's `curl | sh`
  command. Prefer the manual path: download `install.sh` and `SHA256SUMS` of the new tag,
  check `sha256sum -c`, then run `sh install.sh <tag>` as root.
- Every browser session is invalidated once (the old signing secret was never stored):
  sign in again. The first admin password for a fresh database is written to
  `initial-admin-password` next to the database.
- Upgrade through the tag: `install.sh` and `SHA256SUMS` are assets of the release; verify
  `install.sh` with `sha256sum -c`, then run it (it verifies the archive against the same
  file). The `master` branch is not part of the upgrade path any more.
- **Going back is safe for the database.** The migrations of this release only add columns
  and tables (`admin_sessions`, `api_tokens`, new job/app columns, indexes); the previous
  binary starts on the upgraded database and ignores them (verified by the compatibility
  check of the release process, SC-013). Roll back by reinstalling the previous tag the same
  way and sign in again. Keep a copy of the database file before upgrading anyway.

## Apps that were valid before and are not any more

Input validation is stricter (binary path, service name, GitHub repo, artifact name).
An app that is already stored with a value that no longer passes (for example a space,
`$`, `%` or a system directory in the binary path, or a service name starting with `-`)
**cannot be deployed until it is edited**. The job fails with
`edit app: <field> is invalid (...)`. Open the app, correct the field, save.

## Deploy behaviour changes

- The health check needs three consecutive `active` samples two seconds apart with an
  unchanged `NRestarts` (30 s cap); an optional per-app `healthUrl` must also answer 2xx.
- The binary is replaced atomically (hard-link backup, fsync, rename); a crashed deploy is
  recovered at the next start. Artifacts must be ELF executables for the host
  architecture; when the release publishes a `digest` it must match. New apps default to
  `requireDigest=true`; existing apps keep it off (an asset without a digest is deployed
  with a warning and `artifact_verified=0`).
- WebSockets are closed with code 4401 when the session ends and 4409 for an unsupported
  `?protocol=` value.
