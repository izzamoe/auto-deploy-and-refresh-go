#!/bin/sh

set -eu

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

: "${AUTO_DEPLOY_GITHUB_API_BASE:=https://api.github.com}"
: "${AUTO_DEPLOY_GITHUB_DOWNLOAD_BASE:=https://github.com}"
: "${AUTO_DEPLOY_GITHUB_RAW_BASE:=https://raw.githubusercontent.com}"
: "${AUTO_DEPLOY_SYSTEMD_DIR:=/run/systemd/system}"

: "${AUTO_DEPLOY_CURL_BIN:=curl}"
: "${AUTO_DEPLOY_UNZIP_BIN:=unzip}"
: "${AUTO_DEPLOY_INSTALL_BIN:=install}"
: "${AUTO_DEPLOY_SYSTEMCTL_BIN:=systemctl}"
: "${AUTO_DEPLOY_MKTEMP_BIN:=mktemp}"
: "${AUTO_DEPLOY_SED_BIN:=sed}"
: "${AUTO_DEPLOY_TR_BIN:=tr}"
: "${AUTO_DEPLOY_UNAME_BIN:=uname}"
: "${AUTO_DEPLOY_ID_BIN:=id}"
: "${AUTO_DEPLOY_SHA256SUM_BIN:=sha256sum}"

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
CONTRACT_FILE="${SCRIPT_DIR}/release-contract.sh"

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

if [ -f "$CONTRACT_FILE" ]; then
    # shellcheck source=./release-contract.sh
    . "$CONTRACT_FILE"
fi

usage() {
    cat <<EOF
Usage: install.sh [vX.Y.Z]

Installs auto-deploy on Linux with systemd.

Contract:
  - default version: latest stable GitHub release
  - explicit version override: positional tag argument matching ${AUTO_DEPLOY_TAG_GLOB}
  - supported OS/arch: Linux amd64, Linux arm64
  - supported assets: ${AUTO_DEPLOY_ASSET_AMD64}, ${AUTO_DEPLOY_ASSET_ARM64}
  - install path: ${AUTO_DEPLOY_INSTALL_DIR}
  - upgrade behavior: overwrite binary and service unit, preserve existing ${AUTO_DEPLOY_ENV_PATH}
  - runtime files fetched separately from raw.githubusercontent.com at the same tag
  - systemd activation: systemctl daemon-reload && systemctl enable ${AUTO_DEPLOY_SERVICE_UNIT_NAME} && systemctl restart ${AUTO_DEPLOY_SERVICE_UNIT_NAME}
EOF
}

# verify_archive_checksum ARCHIVE ASSET_NAME SHA256SUMS_FILE: the release publishes
# SHA256SUMS (sha256sum format, FR-046, FR-137); an archive that does not match is
# never unpacked or installed.
verify_archive_checksum() {
    archive=$1
    asset=$2
    sums=$3
    expected=""
    while read -r sum name; do
        name=${name#\*}
        if [ "$name" = "$asset" ]; then
            expected=$sum
        fi
    done < "$sums"
    if [ -z "$expected" ]; then
        fail "checksum for $asset not found in SHA256SUMS; refusing to install"
    fi
    actual=$("$AUTO_DEPLOY_SHA256SUM_BIN" "$archive") || fail "failed to hash $archive"
    actual=${actual%% *}
    expected=$(printf '%s' "$expected" | "$AUTO_DEPLOY_TR_BIN" 'A-F' 'a-f')
    actual=$(printf '%s' "$actual" | "$AUTO_DEPLOY_TR_BIN" 'A-F' 'a-f')
    if [ "$actual" != "$expected" ]; then
        fail "checksum mismatch for $asset: expected $expected, got $actual; refusing to install"
    fi
    printf 'Verified SHA-256 of %s\n' "$asset"
}

fail() {
    printf '%s\n' "$1" >&2
    exit 1
}

has_command() {
    case "$1" in
        */*) [ -x "$1" ] ;;
        *) command -v "$1" >/dev/null 2>&1 ;;
    esac
}

require_command() {
    if ! has_command "$1"; then
        fail "missing required command: $2"
    fi
}

require_root() {
    if [ "$("$AUTO_DEPLOY_ID_BIN" -u)" -ne 0 ]; then
        fail 'install.sh must run as root (use sudo).'
    fi
}

require_linux() {
    os=$("$AUTO_DEPLOY_UNAME_BIN" -s)
    if [ "$os" != "Linux" ]; then
        fail "unsupported operating system: $os (supported: Linux only)"
    fi
}

require_systemd() {
    if [ ! -d "$AUTO_DEPLOY_SYSTEMD_DIR" ]; then
        fail "unsupported init system: systemd runtime not detected at $AUTO_DEPLOY_SYSTEMD_DIR"
    fi

    if ! "$AUTO_DEPLOY_SYSTEMCTL_BIN" --version >/dev/null 2>&1; then
        fail 'systemctl is installed but unusable on this host'
    fi
}

detect_arch() {
    raw_arch=$("$AUTO_DEPLOY_UNAME_BIN" -m)
    arch=$(auto_deploy_normalize_arch "$raw_arch") || fail "unsupported architecture: $raw_arch (supported: amd64, arm64)"
    printf '%s\n' "$arch"
}

latest_release_api_url() {
    printf '%s/repos/%s/releases/latest\n' "$AUTO_DEPLOY_GITHUB_API_BASE" "$AUTO_DEPLOY_REPO"
}

release_asset_url() {
    printf '%s/%s/releases/download/%s/%s\n' "$AUTO_DEPLOY_GITHUB_DOWNLOAD_BASE" "$AUTO_DEPLOY_REPO" "$1" "$2"
}

release_raw_url() {
    printf '%s/%s/%s/%s\n' "$AUTO_DEPLOY_GITHUB_RAW_BASE" "$AUTO_DEPLOY_REPO" "$1" "$2"
}

download_to_file() {
    url=$1
    destination=$2
    label=$3

    printf 'Fetching %s from %s\n' "$label" "$url"
    if ! "$AUTO_DEPLOY_CURL_BIN" -fsSL "$url" -o "$destination"; then
        fail "failed to download $label from $url"
    fi
}

resolve_version() {
    if [ "$#" -gt 1 ]; then
        usage >&2
        exit 1
    fi

    if [ "$#" -eq 1 ]; then
        case "$1" in
            v*) printf '%s\n' "$1" ;;
            *)
                printf 'version override must be a git tag matching %s (got %s)\n' "$AUTO_DEPLOY_TAG_GLOB" "$1" >&2
                exit 1
                ;;
        esac
        return
    fi

    latest_release_url=$(latest_release_api_url)
    if ! latest_release_json=$("$AUTO_DEPLOY_CURL_BIN" -fsSL "$latest_release_url"); then
        fail "failed to resolve latest stable release from $latest_release_url"
    fi

    version=$(printf '%s' "$latest_release_json" | "$AUTO_DEPLOY_SED_BIN" -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p')
    if [ -z "$version" ]; then
        fail "failed to parse latest stable release tag from $latest_release_url"
    fi

    printf '%s\n' "$version"
}

validate_archive_contract() {
    archive_path=$1
    archive_contents_file=$2

    if ! "$AUTO_DEPLOY_UNZIP_BIN" -Z1 "$archive_path" > "$archive_contents_file"; then
        fail "failed to inspect release archive: $archive_path"
    fi

    archive_contents=$("$AUTO_DEPLOY_TR_BIN" '\n' ' ' < "$archive_contents_file" | "$AUTO_DEPLOY_SED_BIN" 's/[[:space:]]*$//')
    if [ "$archive_contents" != "$AUTO_DEPLOY_BINARY_NAME" ]; then
        fail "release archive contract violation: expected only $AUTO_DEPLOY_BINARY_NAME, got: $archive_contents"
    fi
}

main() {
    if [ "${1:-}" = "-h" ] || [ "${1:-}" = "--help" ]; then
        usage
        exit 0
    fi

    require_command "$AUTO_DEPLOY_CURL_BIN" curl
    require_command "$AUTO_DEPLOY_UNZIP_BIN" unzip
    require_command "$AUTO_DEPLOY_INSTALL_BIN" install
    require_command "$AUTO_DEPLOY_SYSTEMCTL_BIN" systemctl
    require_command "$AUTO_DEPLOY_MKTEMP_BIN" mktemp
    require_command "$AUTO_DEPLOY_SED_BIN" sed
    require_command "$AUTO_DEPLOY_TR_BIN" tr
    require_command "$AUTO_DEPLOY_UNAME_BIN" uname
    require_command "$AUTO_DEPLOY_ID_BIN" id
    require_command "$AUTO_DEPLOY_SHA256SUM_BIN" sha256sum

    require_linux
    arch=$(detect_arch)
    require_systemd
    require_root

    version=$(resolve_version "$@")
    case "$version" in
        v*) ;;
        *) fail "resolved release tag must match $AUTO_DEPLOY_TAG_GLOB (got $version)" ;;
    esac

    asset_name=$(auto_deploy_asset_name_for_arch "$arch") || fail "no release asset defined for architecture: $arch"

    tmp_dir=$("$AUTO_DEPLOY_MKTEMP_BIN" -d)
    trap 'rm -rf "$tmp_dir"' EXIT INT TERM

    archive_path="$tmp_dir/$asset_name"
    service_download_path="$tmp_dir/$AUTO_DEPLOY_SERVICE_UNIT_NAME"
    env_template_download_path="$tmp_dir/$AUTO_DEPLOY_ENV_TEMPLATE_NAME"
    archive_contents_file="$tmp_dir/archive-contents.txt"

    asset_url=$(release_asset_url "$version" "$asset_name")
    service_url=$(release_raw_url "$version" "$AUTO_DEPLOY_SERVICE_UNIT_NAME")
    env_template_url=$(release_raw_url "$version" "$AUTO_DEPLOY_ENV_TEMPLATE_NAME")

    printf 'Installing auto-deploy %s for Linux %s\n' "$version" "$arch"

    download_to_file "$asset_url" "$archive_path" "release asset"

    # FR-046: verify the archive against the release's SHA256SUMS before anything is
    # unpacked or installed. `auto-deploy upgrade` passes the file it already verified.
    sums_path="$tmp_dir/SHA256SUMS"
    if [ -n "${AUTO_DEPLOY_SHA256SUMS_FILE:-}" ]; then
        cp -- "$AUTO_DEPLOY_SHA256SUMS_FILE" "$sums_path" || fail "failed to read $AUTO_DEPLOY_SHA256SUMS_FILE"
        verify_archive_checksum "$archive_path" "$asset_name" "$sums_path"
    elif "$AUTO_DEPLOY_CURL_BIN" -fsSL "$(release_asset_url "$version" SHA256SUMS)" -o "$sums_path"; then
        verify_archive_checksum "$archive_path" "$asset_name" "$sums_path"
    elif [ "${AUTO_DEPLOY_ALLOW_UNVERIFIED:-}" = "1" ]; then
        printf 'WARNING: SHA256SUMS is not published for %s; installing WITHOUT checksum verification (AUTO_DEPLOY_ALLOW_UNVERIFIED=1)\n' "$version" >&2
    else
        fail "failed to download SHA256SUMS for $version; refusing to install an unverified archive (set AUTO_DEPLOY_ALLOW_UNVERIFIED=1 to override)"
    fi
    validate_archive_contract "$archive_path" "$archive_contents_file"

    if ! "$AUTO_DEPLOY_UNZIP_BIN" -q "$archive_path" -d "$tmp_dir/unpacked"; then
        fail "failed to extract release archive: $archive_path"
    fi

    download_to_file "$service_url" "$service_download_path" "$AUTO_DEPLOY_SERVICE_UNIT_NAME"
    download_to_file "$env_template_url" "$env_template_download_path" "$AUTO_DEPLOY_ENV_TEMPLATE_NAME"

    "$AUTO_DEPLOY_INSTALL_BIN" -d -m 755 "$AUTO_DEPLOY_INSTALL_DIR"
    "$AUTO_DEPLOY_INSTALL_BIN" -m 755 "$tmp_dir/unpacked/$AUTO_DEPLOY_BINARY_NAME" "$AUTO_DEPLOY_BINARY_PATH"
    "$AUTO_DEPLOY_INSTALL_BIN" -D -m 644 "$service_download_path" "$AUTO_DEPLOY_SERVICE_UNIT_PATH"

    if [ -f "$AUTO_DEPLOY_ENV_PATH" ]; then
        printf 'Preserving existing %s\n' "$AUTO_DEPLOY_ENV_PATH"
    else
        "$AUTO_DEPLOY_INSTALL_BIN" -D -m 644 "$env_template_download_path" "$AUTO_DEPLOY_ENV_PATH"
        printf 'Created %s from release template\n' "$AUTO_DEPLOY_ENV_PATH"
    fi

    "$AUTO_DEPLOY_SYSTEMCTL_BIN" daemon-reload
    "$AUTO_DEPLOY_SYSTEMCTL_BIN" enable "$AUTO_DEPLOY_SERVICE_UNIT_NAME"
    "$AUTO_DEPLOY_SYSTEMCTL_BIN" restart "$AUTO_DEPLOY_SERVICE_UNIT_NAME"
    "$AUTO_DEPLOY_SYSTEMCTL_BIN" is-active --quiet "$AUTO_DEPLOY_SERVICE_UNIT_NAME" || fail "service failed to start: $AUTO_DEPLOY_SERVICE_UNIT_NAME"

    printf 'Installed %s to %s\n' "$version" "$AUTO_DEPLOY_BINARY_PATH"
}

main "$@"
