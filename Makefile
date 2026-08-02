.PHONY: build build-arm64 build-linux-amd64 build-linux-arm64 package-release release release-validate-local vet clean test admin-ui lint llms llms-check

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
	npm install
	npm run admin:build

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
	rm -rf $(BUILD_DIR) auto-deploy auto-deploy-arm64 web/admin/dist
	mkdir -p web/admin/dist
	touch web/admin/dist/.keep

test: admin-ui
	go test ./...
