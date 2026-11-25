# Release notes (next release)

Drafted by the executor; T099 completes it. The first upgrade to this release still goes
through the previous binary's mechanism (see "Upgrading").

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
