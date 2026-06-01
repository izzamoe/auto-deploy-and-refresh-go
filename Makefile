.PHONY: build build-arm64 build-linux-amd64 build-linux-arm64 package-release release release-validate-local vet clean test admin-ui lint

BUILD_DIR := dist
STAGING_DIR := $(BUILD_DIR)/staging
RELEASE_DIR := $(BUILD_DIR)/release
RELEASE_GOFLAGS := -trimpath -buildvcs=false

admin-ui:
	npm install
	npm run admin:build

build: admin-ui
	go build $(RELEASE_GOFLAGS) -o auto-deploy ./cmd/server/

build-arm64: admin-ui
	GOOS=linux GOARCH=arm64 go build $(RELEASE_GOFLAGS) -o auto-deploy-arm64 ./cmd/server/

build-linux-amd64: admin-ui
	mkdir -p $(STAGING_DIR)/linux_amd64
	GOOS=linux GOARCH=amd64 go build $(RELEASE_GOFLAGS) -o $(STAGING_DIR)/linux_amd64/auto-deploy ./cmd/server/

build-linux-arm64: admin-ui
	mkdir -p $(STAGING_DIR)/linux_arm64
	GOOS=linux GOARCH=arm64 go build $(RELEASE_GOFLAGS) -o $(STAGING_DIR)/linux_arm64/auto-deploy ./cmd/server/

package-release:
	python3 ./release-package.py

release: build-linux-amd64 build-linux-arm64 package-release

release-validate-local:
	./scripts/validate-release-local.sh

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
