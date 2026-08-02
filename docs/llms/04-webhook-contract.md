# Webhook reference

> `POST /webhook` with `Authorization: Bearer <app-token>` and `{"tag": "v1.2.3"}`. 202 means queued, 200 means the same tag is already in flight, and neither means the deploy succeeded.

This is the only endpoint your pipeline calls. Everything else on this server is the
admin UI and requires a session cookie your pipeline does not have.

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
| 503 | `error` | The app's queue is full (default 10 pending). | Back off and retry; do not fail hard. |
| 500 | `error` | Internal failure while enqueueing. | Retry once, then fail. |

A 401 is deliberately identical whether the token is malformed or simply unknown — it
never reveals which applications exist.

## What happens after 202

The queued job runs through these phases, visible on the app's history page:

1. **Download** — `https://github.com/{repo}/releases/download/{tag}/{artifact}`
2. **Validate** — non-empty, ≥4 bytes, ELF magic, ≤100 MiB
3. **Backup** — current binary copied to `<binary>.bak`
4. **Install** — new binary written and `chmod 0755`
5. **Restart** — `systemctl restart <unit>`
6. **Health check** — up to 5 attempts, sleeping 2 s before each, requiring
   `systemctl is-active <unit>` to report `active`
7. **Rollback** — on health-check failure, `.bak` is restored and the unit restarted again

The health check gives the service roughly 10 seconds to reach `active`. A unit that takes
longer to become ready will be rolled back even though the binary is fine.
