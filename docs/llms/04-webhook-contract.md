# Webhook reference

> `POST /webhook` with `Authorization: Bearer <app-token>` and `{"tag": "v1.2.3"}`. 202 means queued, 200 means the same tag is already in flight, and neither means the deploy succeeded.

This is the only endpoint your pipeline calls. Everything else on this server is the
admin UI and its JSON API (`/admin/api/v1`), which needs an admin session or an admin API
token that your pipeline does not have. The webhook token is not accepted there either.

## Request

```http
POST /webhook HTTP/1.1
Host: <this server>
Authorization: Bearer <token issued for one application>
Content-Type: application/json

{"tag": "v1.2.3"}
```

`tag` must be the tag the GitHub Release was published under — it is substituted directly
into the asset download URL. The body accepts no other fields; artifact name, repository,
and target service all come from the app's stored configuration, not from the request.

## Responses

| Status | `status` | Meaning | What your pipeline should do |
| --- | --- | --- | --- |
| 202 | `queued` | Accepted; the deploy runs in the background. | Succeed. |
| 200 | `duplicate` | This tag is already queued or running for this app. | Succeed — it is not an error. |
| 400 | `error` | Body unparseable, or `tag` missing/empty. | Fail; the request is malformed. |
| 401 | `error` | No `Bearer` prefix, or no app matches the token hash. | Fail; the token is wrong or was rotated. |
| 429 | `error` | Too many failed authentications from this address (30 per minute); carries `Retry-After`. Requests with a valid token are not counted. | Fix the token; retry after the delay. |
| 503 | `error` | The app's queue is full (default 10 pending). | Back off and retry; do not fail hard. |
| 500 | `error` | Internal failure while enqueueing. The body is the fixed `internal error`; details are in the server log. | Retry once, then fail. |

Every error body also carries a machine-readable `code` (for example `rate_limited`,
`internal_error`) and every response an `X-Request-ID` header; clients that ignore both keep
working. A 401 is deliberately identical whether the token is malformed or simply unknown —
it never reveals which applications exist.

## What happens after 202

The queued job runs through these phases, visible on the app's history page:

1. **Download** — `https://github.com/{repo}/releases/download/{tag}/{artifact}`
2. **Validate** — non-empty, ≥4 bytes, ELF magic for the host architecture, ≤100 MiB; when
   the release publishes a SHA-256 `digest` for the asset it must match (an app with
   `requireDigest` refuses an asset that has none)
3. **Backup** — current binary copied to `<binary>.bak`
4. **Install** — new binary written, `chmod 0755` and swapped in atomically (a crash in
   the middle is recovered at the next start)
5. **Restart** — `systemctl restart <unit>`
6. **Health check** — three consecutive samples two seconds apart in which
   `systemctl is-active <unit>` reports `active` and the restart counter (`NRestarts`)
   has not moved, within a 30 s cap; when the app has a `healthUrl` it must also answer 2xx
7. **Rollback** — on health-check failure, `.bak` is restored and the unit restarted again

The health check gives the service up to 30 seconds to become `active` and stay up for
three samples. A unit that takes longer to become ready, or that restarts itself while the
samples are taken (a crash loop under `Restart=always`), is rolled back even though the
binary is fine.
