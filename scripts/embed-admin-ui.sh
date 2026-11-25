#!/bin/sh
# Copy the Next static export (web/admin/out) into the directory the Go binary
# embeds (internal/admin/web/admin/dist). The tracked .keep file MUST survive:
# `//go:embed all:web/admin/dist` fails with "contains no embeddable files" when
# the directory has no tracked file (Q-12: the build output itself is not committed).
set -eu

root="$(cd "$(dirname "$0")/.." && pwd)"
src="$root/web/admin/out"
dst="$root/internal/admin/web/admin/dist"

if [ ! -f "$src/index.html" ] || [ ! -f "$src/404.html" ]; then
	echo "embed-admin-ui: $src has no index.html/404.html; run 'next build web/admin' first" >&2
	exit 1
fi

mkdir -p "$dst"
# Clean everything except .keep (depth-first delete).
find "$dst" -mindepth 1 ! -path "$dst/.keep" -delete
cp -R "$src"/. "$dst"/
[ -f "$dst/.keep" ] || : > "$dst/.keep"
echo "embed-admin-ui: $(find "$dst" -type f | wc -l) files copied to internal/admin/web/admin/dist"
