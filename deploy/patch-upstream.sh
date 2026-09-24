#!/bin/sh
set -eu
src="$1"
patch_dir="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
if grep -Fq 'if u, ok := urls[specs[i].Type]; ok && u != "" {' "$src/transport_spec.go"; then
  exit 0
fi
git -C "$src" apply "$patch_dir/patches/0001-preserve-conf-transport-urls.patch"
