# Failure symptoms and their causes

> Most failures are the artifact contract, not the pipeline. Map the symptom to the cause here before changing the workflow.

## The webhook call itself failed

| Symptom | Cause | Fix |
| --- | --- | --- |
| `401` | Token rotated in the admin UI, or `DEPLOY_TOKEN` secret holds a stale value. Also raised when the `Bearer ` prefix is missing. | Reissue the token and update the CI secret. |
| `400 missing or empty tag` | The JSON body was not sent, or `tag` interpolated to an empty string — usually a workflow triggered by something other than a tag push, so `GITHUB_REF_NAME` is a branch. | Restrict the trigger to `push: tags:`, or derive the tag explicitly. |
| `503 queue full` | More than the configured maximum (default 10) deploys pending for this app. | Back off and retry. Usually means earlier deploys are stuck or the app is being spammed. |
| Connection refused / timeout | The webhook URL is wrong, or the server is not publicly reachable from GitHub's runners. | Confirm the URL with the owner. |

## The webhook returned 202 but nothing deployed

The pipeline is done at this point; these fail inside the server and show up on the app's
history page.

| Server-side error | Cause |
| --- | --- |
| `download failed` / 404 | Asset filename does not match the configured artifact name **exactly**, or the tag has no such asset, or the release was still uploading when the webhook fired. |
| `not an ELF executable` | The asset is an archive (`.zip`, `.tar.gz`), an installer, or a non-Linux build. Publish the raw binary. |
| `empty artifact` / `too small` | The build produced a zero-byte or truncated file — usually a build step that failed without failing the job. |
| `download too large` | Asset exceeds 100 MiB. Build with `-ldflags "-s -w"`. |
| `restart service` error | The systemd unit name configured for the app does not exist on the host. Owner-side configuration, not a pipeline problem. |
| Rolled back after health check | The unit did not report `active` within ~10 seconds. |

## Diagnosing a rollback

A rollback means the binary downloaded, validated, and installed cleanly — so the artifact
contract was satisfied and the problem is the application itself. Likely causes, in order:

1. **Wrong architecture.** A binary for the wrong arch is a valid ELF file and installs
   fine, then fails to execute. This host is `{{GOOS}}/{{GOARCH}}`; confirm the build used
   exactly that, and that `file <asset>` reported `{{ARCH_UNAME}}`.
2. **Missing runtime configuration.** The app crashes on startup for want of an
   environment variable. These are managed per app in the admin UI, not by the pipeline.
3. **Slow startup.** The service needs more than ~10 seconds to reach `active`.
4. **Dynamic linking.** A binary built with `CGO_ENABLED=1` may need shared libraries the
   host lacks. Build with `CGO_ENABLED=0` when possible.

Note that a unit with `Restart=always` can report `active` while crash-looping, so a
"successful" deploy is not proof the application is healthy. Check the app's logs from the
admin UI.

## Deploy never appears at all

If the history page shows no new job, the webhook never reached the server. Check the
pipeline's own logs for the curl step — a non-fatal `curl` without `-f`/`set -e` will
happily swallow a connection error and let the job go green.
