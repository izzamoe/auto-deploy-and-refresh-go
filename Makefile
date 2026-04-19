.PHONY: build build-arm64 build-linux-amd64 build-linux-arm64 package-release release release-validate-local vet clean test

BUILD_DIR := dist
STAGING_DIR := $(BUILD_DIR)/staging
RELEASE_DIR := $(BUILD_DIR)/release
RELEASE_GOFLAGS := -trimpath -buildvcs=false

build:
	go build $(RELEASE_GOFLAGS) -o auto-deploy .

build-arm64:
	GOOS=linux GOARCH=arm64 go build $(RELEASE_GOFLAGS) -o auto-deploy-arm64 .

build-linux-amd64:
	mkdir -p $(STAGING_DIR)/linux_amd64
	GOOS=linux GOARCH=amd64 go build $(RELEASE_GOFLAGS) -o $(STAGING_DIR)/linux_amd64/auto-deploy .

build-linux-arm64:
	mkdir -p $(STAGING_DIR)/linux_arm64
	GOOS=linux GOARCH=arm64 go build $(RELEASE_GOFLAGS) -o $(STAGING_DIR)/linux_arm64/auto-deploy .

package-release:
	python3 ./release-package.py

release: build-linux-amd64 build-linux-arm64 package-release

release-validate-local:
	./scripts/validate-release-local.sh

vet:
	go vet ./...

clean:
	rm -rf $(BUILD_DIR) auto-deploy auto-deploy-arm64

test:
	go test ./...
