.PHONY: dev build build-arm64 build-linux-amd64 build-linux-arm64 package-release release release-validate-local vet clean test admin-ui lint llms llms-check \
	fe-install fe-typecheck fe-test api-types-install api-types api-types-check vuln e2e ci

GOVULNCHECK_VERSION := v1.10.0
API_SPEC := api/admin-api.v1.openapi.yaml
API_TYPES := web/admin/src/lib/api/schema.gen.ts

BUILD_DIR := dist
STAGING_DIR := $(BUILD_DIR)/staging
RELEASE_DIR := $(BUILD_DIR)/release
RELEASE_GOFLAGS := -trimpath -buildvcs=false

# VERSION is stamped into the binary so /llms.txt reports the running build. The
# generated llms.txt keeps a {{VERSION}} placeholder instead of a literal version,
# which is what makes the llms-check drift gate stable across releases.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X github.com/izzamoe/auto-deploy/internal/llmstxt.Version=$(VERSION)

admin-ui:
	npm ci
	npm run admin:build

# Development (D17): open http://127.0.0.1:8080/admin/. The Go backend must listen on
# :9000 (default LISTEN_ADDR; start it in another terminal with `go run ./cmd/server`).
# `next dev` serves the UI with HMR on :3000; cmd/devproxy (loopback only) is the single
# origin that sends API/events/webhook to the backend and the rest to Next.
dev:
	@trap 'kill 0' INT TERM EXIT; \
	npm run admin:dev & \
	go run ./cmd/devproxy -listen 127.0.0.1:8080 -backend http://127.0.0.1:9000 -frontend http://127.0.0.1:3000 & \
	wait

build: admin-ui
	go build $(RELEASE_GOFLAGS) -ldflags "$(LDFLAGS)" -o auto-deploy ./cmd/server/

build-arm64: admin-ui
	GOOS=linux GOARCH=arm64 go build $(RELEASE_GOFLAGS) -ldflags "$(LDFLAGS)" -o auto-deploy-arm64 ./cmd/server/

build-linux-amd64: admin-ui
	mkdir -p $(STAGING_DIR)/linux_amd64
	GOOS=linux GOARCH=amd64 go build $(RELEASE_GOFLAGS) -ldflags "$(LDFLAGS)" -o $(STAGING_DIR)/linux_amd64/auto-deploy ./cmd/server/

build-linux-arm64: admin-ui
	mkdir -p $(STAGING_DIR)/linux_arm64
	GOOS=linux GOARCH=arm64 go build $(RELEASE_GOFLAGS) -ldflags "$(LDFLAGS)" -o $(STAGING_DIR)/linux_arm64/auto-deploy ./cmd/server/

package-release:
	python3 ./release-package.py

release: build-linux-amd64 build-linux-arm64 package-release

release-validate-local:
	./scripts/validate-release-local.sh

# Regenerate the published llms.txt / llms-full.txt from docs/llms/*.md and the
# route table extracted from the Go sources. Commit the result.
llms:
	go run ./cmd/llmsgen -root . -out internal/llmstxt

# Drift gate for CI: regenerate into a scratch directory and diff against what is
# committed. Fails when a route or a curated document changed without `make llms`.
llms-check:
	@tmp=$$(mktemp -d) && \
	go run ./cmd/llmsgen -root . -out "$$tmp" && \
	if ! diff -u internal/llmstxt/llms.txt "$$tmp/llms.txt" || \
	   ! diff -u internal/llmstxt/llms-full.txt "$$tmp/llms-full.txt"; then \
		rm -rf "$$tmp"; \
		echo "llms.txt is stale — run 'make llms' and commit the result." >&2; \
		exit 1; \
	fi; \
	rm -rf "$$tmp"; \
	echo "llms.txt is up to date."

vet:
	go vet ./...

lint:
	golangci-lint run --timeout 5m

clean:
	rm -rf $(BUILD_DIR) auto-deploy auto-deploy-arm64 web/admin/out web/admin/.next
	find internal/admin/web/admin/dist -mindepth 1 ! -name .keep -delete 2>/dev/null || true

# Go tests. Deliberately independent of admin-ui: tests must not rewrite the
# tracked internal/admin/web/admin/dist (the built UI is not embedded-by-test).
test:
	go test -race -count=1 ./...

# Known-vulnerability gate (Go). Exit 0 only when govulncheck reports nothing.
vuln:
	go run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) ./...

# --- Frontend gates (current stack: Vite until the Next.js migration) ---
fe-install:
	npm ci

fe-typecheck:
	npx tsc --noEmit -p web/admin/tsconfig.json

fe-test:
	npx vitest run web/admin --config web/admin/vitest.config.ts

# --- OpenAPI contract -> TypeScript types ---
# openapi-typescript 7.x needs the classic TypeScript JS API (peer typescript ^5),
# which TypeScript 7 does not ship, so the generator is isolated in
# tools/openapi-types (own typescript 5.9.3). Paths below are relative to the repo root.
api-types-install:
	npm --prefix tools/openapi-types ci

api-types: api-types-install
	npm --prefix tools/openapi-types exec -- openapi-typescript $(API_SPEC) -o $(API_TYPES)

# Drift gate: regenerate into a scratch directory and diff against the committed file.
api-types-check: api-types-install
	@tmp=$$(mktemp -d) && \
	npm --prefix tools/openapi-types exec -- openapi-typescript $(API_SPEC) -o "$$tmp/schema.gen.ts" >/dev/null && \
	if ! diff -u $(API_TYPES) "$$tmp/schema.gen.ts"; then \
		rm -rf "$$tmp"; \
		echo "$(API_TYPES) is stale — run 'make api-types' and commit the result." >&2; \
		exit 1; \
	fi; \
	rm -rf "$$tmp"; \
	echo "$(API_TYPES) is up to date."

# Browser tests (Playwright): the four gates run explicitly (RV3-13); the `visual` project
# only when a local baseline exists (it is not committed, Q-25). `perf` (T104) is never run here.
# 002 T087: `realtime` too, latency block included (local; the CI skips it, RV2-15).
e2e:
	npx playwright test --project=smoke --project=routing --project=session --project=a11y --project=realtime
	@if ls tests/visual.spec.ts-snapshots/*.png >/dev/null 2>&1; then \
		npx playwright test --project=visual; \
	else \
		echo "visual: no local baseline in tests/visual.spec.ts-snapshots, skipped"; \
	fi

# Full local gate, same order as CI.
ci: llms-check vet test vuln fe-install fe-typecheck fe-test api-types-check build e2e
