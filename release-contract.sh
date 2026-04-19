#!/bin/sh

# Release and installer contract for auto-deploy.
# Downstream release automation should treat this file as the machine-readable
# source of truth for tag shape, asset names, install paths, and fetched files.


: "${AUTO_DEPLOY_REPO:=izzamoe/auto-deploy-and-refresh-go}"
: "${AUTO_DEPLOY_TAG_GLOB:=v*}"

: "${AUTO_DEPLOY_BINARY_NAME:=auto-deploy}"
: "${AUTO_DEPLOY_ASSET_AMD64:=auto-deploy_linux_amd64.zip}"
: "${AUTO_DEPLOY_ASSET_ARM64:=auto-deploy_linux_arm64.zip}"

: "${AUTO_DEPLOY_INSTALL_DIR:=/opt/auto-deploy}"
: "${AUTO_DEPLOY_BINARY_PATH:=${AUTO_DEPLOY_INSTALL_DIR}/${AUTO_DEPLOY_BINARY_NAME}}"
: "${AUTO_DEPLOY_SERVICE_UNIT_PATH:=/etc/systemd/system/auto-deploy.service}"
: "${AUTO_DEPLOY_ENV_PATH:=/etc/auto-deploy.env}"
: "${AUTO_DEPLOY_ENV_TEMPLATE_NAME:=auto-deploy.env.example}"
: "${AUTO_DEPLOY_SERVICE_UNIT_NAME:=auto-deploy.service}"

# Contract rule: release archives contain the binary only. Service and env
# template are fetched separately from the exact matching git ref/tag.

auto_deploy_asset_name_for_arch() {
    case "$1" in
        amd64) printf '%s\n' "$AUTO_DEPLOY_ASSET_AMD64" ;;
        arm64) printf '%s\n' "$AUTO_DEPLOY_ASSET_ARM64" ;;
        *) return 1 ;;
    esac
}

auto_deploy_normalize_arch() {
    case "$1" in
        x86_64|amd64) printf 'amd64\n' ;;
        aarch64|arm64) printf 'arm64\n' ;;
        *) return 1 ;;
    esac
}
