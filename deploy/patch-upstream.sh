#!/bin/sh
set -eu
src="$1"
patch_dir="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
if ! grep -Fq 'if u, ok := urls[specs[i].Type]; ok && u != "" {' "$src/transport_spec.go"; then
  git -C "$src" apply "$patch_dir/patches/0001-preserve-conf-transport-urls.patch"
fi

# Yandex Board switched /api from form-encoded to JSON requests. Its own
# whiteboard.lib.js now calls anypost(..., { json: true }). Keep this patch
# conditional so upstream can incorporate the same fix independently.
if grep -Fq 'application/x-www-form-urlencoded; charset=UTF-8' "$src/transport/yandex/boards.go"; then
  git -C "$src" apply "$patch_dir/patches/0002-board-json-api.patch"
fi
