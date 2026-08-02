# The pipeline to generate

> Build for Linux, publish the release with the asset named exactly as configured, then call the webhook — in that order. Treat HTTP 200 (`duplicate`) as success alongside 202.

## Ordering rule

Publish the release **before** calling the webhook. The server downloads the asset the
moment it dequeues the job, which can be within a second of the call. A webhook fired
before the asset finishes uploading races the release and fails with a 404.

## GitHub Actions — complete template

The architecture is already filled in for this host. The only value left to substitute is
`<ARTIFACT_NAME>` — ask the repository owner, or read it off the app's edit page.

```yaml
name: Release and deploy

on:
  push:
    tags: ['v*']

permissions:
  contents: write   # required to publish the release

jobs:
  release:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4

      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod

      - run: go test ./...

      # The output filename must equal the configured artifact name exactly:
      # no version, no SHA, no extension, and no compression.
      - name: Build binary for the deploy host
        run: |
          GOOS={{GOOS}} GOARCH={{GOARCH}} CGO_ENABLED=0 \
            go build -trimpath -ldflags "-s -w" -o <ARTIFACT_NAME> ./cmd/app

      - name: Verify the artifact contract before publishing
        run: |
          set -euo pipefail
          file <ARTIFACT_NAME> | grep -q ELF || { echo "not an ELF binary"; exit 1; }
          file <ARTIFACT_NAME> | grep -q '{{ARCH_UNAME}}' || { echo "wrong architecture for the deploy host"; exit 1; }
          size=$(stat -c%s <ARTIFACT_NAME>)
          [ "$size" -le 104857600 ] || { echo "artifact is ${size}b, over the 100MiB limit"; exit 1; }

      - name: Publish GitHub Release
        uses: softprops/action-gh-release@v2
        with:
          files: <ARTIFACT_NAME>
          fail_on_unmatched_files: true

      # Only now is the asset downloadable.
      - name: Trigger deploy
        env:
          DEPLOY_URL: ${{ secrets.DEPLOY_URL }}
          DEPLOY_TOKEN: ${{ secrets.DEPLOY_TOKEN }}
        run: |
          set -euo pipefail
          code=$(curl -sS -o response.json -w '%{http_code}' -X POST "$DEPLOY_URL" \
            -H "Authorization: Bearer $DEPLOY_TOKEN" \
            -H 'Content-Type: application/json' \
            -d "{\"tag\": \"${GITHUB_REF_NAME}\"}")
          cat response.json
          case "$code" in
            202) echo "deploy queued" ;;
            200) echo "already queued for this tag — treating as success" ;;
            503) echo "::error::deploy queue is full, retry later"; exit 1 ;;
            401) echo "::error::DEPLOY_TOKEN rejected — was it rotated in the admin UI?"; exit 1 ;;
            *)   echo "::error::webhook returned $code"; exit 1 ;;
          esac
```

## Repository secrets to create

| Secret | Value |
| --- | --- |
| `DEPLOY_URL` | Full webhook URL, e.g. `https://deploy.example.com/webhook` |
| `DEPLOY_TOKEN` | The per-application token from the admin UI |

Both are per application. One token per app keeps a leak scoped to a single deploy target.
The token is stored on the server only as a SHA-256 hash, so it cannot be recovered — if
it is lost, the owner reissues it and you update the secret.

## Non-Go projects

The contract is language-agnostic: it only asks for a self-contained Linux ELF executable.

- **Rust** — `cargo build --release --target {{RUST_TARGET}}`, then rename
  `target/{{RUST_TARGET}}/release/<bin>` to the artifact name.
- **Node/Python** — a plain interpreter script is not an ELF file and will be rejected.
  Produce a single-file executable first (`bun build --compile`, `pkg`, `pyinstaller
  --onefile`) and publish that.
- **Anything else** — if `file <asset>` does not say `ELF`, this server cannot deploy it.

## Verifying the rollout

A 202 means *accepted*, not *deployed*. The webhook has no way to report the final
outcome. If the pipeline must gate on a successful rollout, have the owner check the app's
history page in the admin UI, or poll the admin API with an admin session — the deploy
token does not grant access to those endpoints.
