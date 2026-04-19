#!/usr/bin/env python3

from __future__ import annotations

import os
import sys
import zipfile
from pathlib import Path


BINARY_NAME = "auto-deploy"
FIXED_ZIP_DT = (1980, 1, 1, 0, 0, 0)


def package_arch(repo_root: Path, build_dir: Path, arch: str) -> None:
    staging_path = build_dir / "staging" / f"linux_{arch}" / BINARY_NAME
    release_dir = build_dir / "release"
    archive_path = release_dir / f"auto-deploy_linux_{arch}.zip"

    if not staging_path.is_file():
        print(f"missing staged binary for {arch}: {staging_path}", file=sys.stderr)
        raise SystemExit(1)

    release_dir.mkdir(parents=True, exist_ok=True)
    if archive_path.exists():
        archive_path.unlink()

    info = zipfile.ZipInfo(BINARY_NAME, date_time=FIXED_ZIP_DT)
    info.compress_type = zipfile.ZIP_DEFLATED
    info.create_system = 3
    info.external_attr = 0o755 << 16

    with zipfile.ZipFile(archive_path, mode="w", compression=zipfile.ZIP_DEFLATED, compresslevel=9) as zf:
        zf.writestr(info, staging_path.read_bytes())

    print(f"created {archive_path}")


def main() -> int:
    repo_root = Path(__file__).resolve().parent
    build_dir = Path(os.environ.get("BUILD_DIR", repo_root / "dist"))

    package_arch(repo_root, build_dir, "amd64")
    package_arch(repo_root, build_dir, "arm64")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
