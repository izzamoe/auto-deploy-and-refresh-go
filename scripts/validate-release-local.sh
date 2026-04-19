#!/bin/sh

set -eu

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
ROOT_DIR=$(CDPATH= cd -- "$SCRIPT_DIR/.." && pwd)

cd "$ROOT_DIR"
. ./release-contract.sh
export AUTO_DEPLOY_REPO
export AUTO_DEPLOY_TAG_GLOB
export AUTO_DEPLOY_BINARY_NAME
export AUTO_DEPLOY_ASSET_AMD64
export AUTO_DEPLOY_ASSET_ARM64
export AUTO_DEPLOY_INSTALL_DIR
export AUTO_DEPLOY_BINARY_PATH
export AUTO_DEPLOY_SERVICE_UNIT_PATH
export AUTO_DEPLOY_ENV_PATH
export AUTO_DEPLOY_ENV_TEMPLATE_NAME
export AUTO_DEPLOY_SERVICE_UNIT_NAME

VERSION=${AUTO_DEPLOY_LOCAL_RELEASE_TAG:-v1.2.3}

case "$VERSION" in
    v*) ;;
    *)
        printf 'AUTO_DEPLOY_LOCAL_RELEASE_TAG must match %s (got %s)\n' "$AUTO_DEPLOY_TAG_GLOB" "$VERSION" >&2
        exit 1
        ;;
esac

log() {
    printf '%s\n' "$1"
}

cleanup() {
    if [ -n "${SYSTEMCTL_LOG:-}" ] && [ -f "$SYSTEMCTL_LOG" ]; then
        printf 'Installer systemctl calls:\n'
        sed 's/^/  /' "$SYSTEMCTL_LOG"
    fi
    if [ -n "${TMP_DIR:-}" ] && [ -d "$TMP_DIR" ]; then
        rm -rf "$TMP_DIR"
    fi
}

trap cleanup EXIT INT TERM

log 'Running test suite'
make test

log 'Building release archives'
make clean
make release

log 'Inspecting release archives'
python3 - <<'PY'
import os
import sys
import zipfile
from pathlib import Path

release_dir = Path('dist/release')
expected = sorted([
    os.environ['AUTO_DEPLOY_ASSET_AMD64'],
    os.environ['AUTO_DEPLOY_ASSET_ARM64'],
])
actual = sorted(path.name for path in release_dir.glob('*.zip'))

if actual != expected:
    print(f'expected release assets {expected}, found {actual}', file=sys.stderr)
    raise SystemExit(1)

for asset in expected:
    archive_path = release_dir / asset
    with zipfile.ZipFile(archive_path) as archive:
        names = archive.namelist()
        if names != [os.environ['AUTO_DEPLOY_BINARY_NAME']]:
            print(
                f"{asset} must contain only {os.environ['AUTO_DEPLOY_BINARY_NAME']}, found {names}",
                file=sys.stderr,
            )
            raise SystemExit(1)

print('Validated release archive names and contents.')
PY

log 'Reviewing workflow logic'
python3 - <<'PY'
from pathlib import Path


def get_job_block(lines: list[str], job_name: str) -> list[str]:
    target = f"  {job_name}:"
    start = None

    for index, line in enumerate(lines):
        if line == target:
            start = index
            break

    if start is None:
        raise SystemExit(f'missing workflow job: {job_name}')

    end = len(lines)
    for index in range(start + 1, len(lines)):
        line = lines[index]
        if line.startswith('  ') and not line.startswith('    '):
            end = index
            break

    return lines[start:end]


def assert_contains(block: list[str], expected: str, context: str) -> None:
    if expected not in block:
        raise SystemExit(f'missing {context}: {expected}')


def assert_step_order(block: list[str], expected_steps: list[str], job_name: str) -> None:
    seen = []
    for line in block:
        if line.startswith('      - name: '):
            seen.append(line.removeprefix('      - name: '))

    cursor = 0
    for step in expected_steps:
        try:
            cursor = seen.index(step, cursor) + 1
        except ValueError as exc:
            raise SystemExit(
                f'{job_name} is missing expected step order {expected_steps}; found {seen}'
            ) from exc


workflow_lines = Path('.github/workflows/release.yml').read_text().splitlines()
build_release = get_job_block(workflow_lines, 'build-release')
publish_release = get_job_block(workflow_lines, 'publish-release')

assert_contains(build_release, '      - name: Run tests', 'build-release validation step')
assert_contains(build_release, '        run: make test', 'build-release test command')
assert_contains(build_release, '      - name: Build release archives', 'build-release packaging step')
assert_contains(build_release, '          make clean', 'build-release clean command')
assert_contains(build_release, '          make release', 'build-release package command')
assert_contains(
    build_release,
    '            echo "Manual dispatch runs validation and packaging only; publish job remains disabled by default."',
    'workflow_dispatch guard message',
)
assert_contains(
    build_release,
    "      - name: Upload release archives for publish job",
    'build-release artifact upload step',
)
assert_contains(
    build_release,
    "        if: github.event_name == 'push'",
    'push-only artifact upload guard',
)
assert_contains(build_release, '          name: release-zips', 'artifact upload name')

assert_step_order(
    build_release,
    [
        'Check out repository',
        'Load release contract',
        'Set up Go',
        'Pre-publish validation',
        'Run tests',
        'Build release archives',
        'Validate packaged release assets',
        'Upload release archives for publish job',
    ],
    'build-release',
)

assert_contains(publish_release, "    if: github.event_name == 'push'", 'publish-release push guard')
assert_contains(publish_release, '    needs: build-release', 'publish-release dependency')
assert_contains(publish_release, '          name: release-zips', 'artifact download name')
assert_contains(
    publish_release,
    "        if: success() && github.event_name == 'push' && startsWith(github.ref, 'refs/tags/')",
    'deploy step publish-only guard',
)
assert_contains(
    publish_release,
    '            dist/release/${{ needs.build-release.outputs.asset_amd64 }}',
    'publish-release amd64 asset reference',
)
assert_contains(
    publish_release,
    '            dist/release/${{ needs.build-release.outputs.asset_arm64 }}',
    'publish-release arm64 asset reference',
)

assert_step_order(
    publish_release,
    [
        'Download release archives',
        'Publish GitHub release',
        'Trigger deployment',
    ],
    'publish-release',
)

print('Validated workflow job gating, artifact flow, and publish-after-package ordering.')
PY

TMP_DIR=$(mktemp -d)
SYSTEMCTL_LOG="$TMP_DIR/systemctl.log"
export SYSTEMCTL_LOG
FAKE_BIN_DIR="$TMP_DIR/fake-bin"
FAKE_RELEASE_ROOT="$TMP_DIR/fake-release"
INSTALL_ROOT="$TMP_DIR/install-root"
SYSTEMD_DIR="$TMP_DIR/systemd"
RUNTIME_ETC_DIR="$TMP_DIR/etc"

mkdir -p "$FAKE_BIN_DIR" "$FAKE_RELEASE_ROOT/repos/$AUTO_DEPLOY_REPO/releases/download/$VERSION" "$FAKE_RELEASE_ROOT/$AUTO_DEPLOY_REPO/$VERSION" "$INSTALL_ROOT" "$SYSTEMD_DIR" "$RUNTIME_ETC_DIR"

cat > "$FAKE_BIN_DIR/id" <<'EOF'
#!/bin/sh
case "$1" in
    -u) printf '0\n' ;;
    *) exit 0 ;;
esac
EOF

cat > "$FAKE_BIN_DIR/uname" <<'EOF'
#!/bin/sh
case "$1" in
    -s) printf 'Linux\n' ;;
    -m) printf 'x86_64\n' ;;
    *) exit 1 ;;
esac
EOF

cat > "$FAKE_BIN_DIR/systemctl" <<'EOF'
#!/bin/sh
printf 'systemctl %s\n' "$*" >> "$SYSTEMCTL_LOG"
exit 0
EOF

chmod +x "$FAKE_BIN_DIR/id" "$FAKE_BIN_DIR/uname" "$FAKE_BIN_DIR/systemctl"

cp "dist/release/$AUTO_DEPLOY_ASSET_AMD64" "$FAKE_RELEASE_ROOT/repos/$AUTO_DEPLOY_REPO/releases/download/$VERSION/$AUTO_DEPLOY_ASSET_AMD64"
cp "dist/release/$AUTO_DEPLOY_ASSET_ARM64" "$FAKE_RELEASE_ROOT/repos/$AUTO_DEPLOY_REPO/releases/download/$VERSION/$AUTO_DEPLOY_ASSET_ARM64"
cp auto-deploy.service "$FAKE_RELEASE_ROOT/$AUTO_DEPLOY_REPO/$VERSION/$AUTO_DEPLOY_SERVICE_UNIT_NAME"
cp auto-deploy.env.example "$FAKE_RELEASE_ROOT/$AUTO_DEPLOY_REPO/$VERSION/$AUTO_DEPLOY_ENV_TEMPLATE_NAME"

cat > "$FAKE_RELEASE_ROOT/repos/$AUTO_DEPLOY_REPO/releases/latest" <<EOF
{"tag_name":"$VERSION"}
EOF

PATH="$FAKE_BIN_DIR:$PATH" \
AUTO_DEPLOY_REPO="$AUTO_DEPLOY_REPO" \
AUTO_DEPLOY_GITHUB_API_BASE="file://$FAKE_RELEASE_ROOT" \
AUTO_DEPLOY_GITHUB_DOWNLOAD_BASE="file://$FAKE_RELEASE_ROOT/repos" \
AUTO_DEPLOY_GITHUB_RAW_BASE="file://$FAKE_RELEASE_ROOT" \
AUTO_DEPLOY_INSTALL_DIR="$INSTALL_ROOT/opt/auto-deploy" \
AUTO_DEPLOY_BINARY_PATH="$INSTALL_ROOT/opt/auto-deploy/auto-deploy" \
AUTO_DEPLOY_SERVICE_UNIT_PATH="$INSTALL_ROOT/etc/systemd/system/auto-deploy.service" \
AUTO_DEPLOY_ENV_PATH="$RUNTIME_ETC_DIR/auto-deploy.env" \
AUTO_DEPLOY_SYSTEMD_DIR="$SYSTEMD_DIR" \
AUTO_DEPLOY_LOCAL_RELEASE_TAG="$VERSION" \
./install.sh >/tmp/auto-deploy-local-install.log 2>&1

if [ ! -f "$INSTALL_ROOT/opt/auto-deploy/auto-deploy" ]; then
    printf 'expected installed binary at %s\n' "$INSTALL_ROOT/opt/auto-deploy/auto-deploy" >&2
    exit 1
fi

if [ ! -f "$INSTALL_ROOT/etc/systemd/system/auto-deploy.service" ]; then
    printf 'expected installed service unit at %s\n' "$INSTALL_ROOT/etc/systemd/system/auto-deploy.service" >&2
    exit 1
fi

if [ ! -f "$RUNTIME_ETC_DIR/auto-deploy.env" ]; then
    printf 'expected initial env file at %s\n' "$RUNTIME_ETC_DIR/auto-deploy.env" >&2
    exit 1
fi

printf 'PRESERVE_ME=1\n' > "$RUNTIME_ETC_DIR/auto-deploy.env"

PATH="$FAKE_BIN_DIR:$PATH" \
AUTO_DEPLOY_REPO="$AUTO_DEPLOY_REPO" \
AUTO_DEPLOY_GITHUB_API_BASE="file://$FAKE_RELEASE_ROOT" \
AUTO_DEPLOY_GITHUB_DOWNLOAD_BASE="file://$FAKE_RELEASE_ROOT/repos" \
AUTO_DEPLOY_GITHUB_RAW_BASE="file://$FAKE_RELEASE_ROOT" \
AUTO_DEPLOY_INSTALL_DIR="$INSTALL_ROOT/opt/auto-deploy" \
AUTO_DEPLOY_BINARY_PATH="$INSTALL_ROOT/opt/auto-deploy/auto-deploy" \
AUTO_DEPLOY_SERVICE_UNIT_PATH="$INSTALL_ROOT/etc/systemd/system/auto-deploy.service" \
AUTO_DEPLOY_ENV_PATH="$RUNTIME_ETC_DIR/auto-deploy.env" \
AUTO_DEPLOY_SYSTEMD_DIR="$SYSTEMD_DIR" \
AUTO_DEPLOY_LOCAL_RELEASE_TAG="$VERSION" \
./install.sh "$VERSION" >/tmp/auto-deploy-local-upgrade.log 2>&1

PRESERVE_FILE="$TMP_DIR/preserve.env"
printf 'PRESERVE_ME=1\n' > "$PRESERVE_FILE"

if ! cmp -s "$RUNTIME_ETC_DIR/auto-deploy.env" "$PRESERVE_FILE"
then
    printf 'existing env file was not preserved during upgrade\n' >&2
    exit 1
fi

if ! grep -q '^systemctl --version$' "$SYSTEMCTL_LOG"; then
    printf 'expected systemctl --version during installer preflight\n' >&2
    exit 1
fi

if ! grep -q '^systemctl daemon-reload$' "$SYSTEMCTL_LOG"; then
    printf 'expected systemctl daemon-reload during install\n' >&2
    exit 1
fi

if ! grep -q '^systemctl enable --now auto-deploy.service$' "$SYSTEMCTL_LOG"; then
    printf 'expected systemctl enable --now auto-deploy.service during install\n' >&2
    exit 1
fi

log 'Validated installer behavior against local file:// release fixtures'
