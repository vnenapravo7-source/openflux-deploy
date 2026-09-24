#!/usr/bin/env bash
set -Eeuo pipefail

UPSTREAM_REPO="${OPENFLUX_UPSTREAM_REPO:-https://github.com/p1neappleXpress/OpenFlux.git}"
CONFIG="${OPENFLUX_CONFIG:-/etc/openflux-deploy/config.json}"
STATE_DIR="${OPENFLUX_STATE_DIR:-/var/lib/openflux-deploy}"
BINARY="${OPENFLUX_BINARY:-$STATE_DIR/bin/openflux}"
FORCE=0
[ "${1:-}" = "--force" ] && FORCE=1

say(){ printf '[OpenFlux update] %s\n' "$*"; }
export GOCACHE="${GOCACHE:-$STATE_DIR/go-cache}"
export GOMODCACHE="${GOMODCACHE:-$STATE_DIR/go-mod}"
export GOPATH="${GOPATH:-$STATE_DIR/go-path}"
if [ "$FORCE" -eq 0 ] && [ -f "$CONFIG" ] && grep -Eq '"auto_update"[[:space:]]*:[[:space:]]*false' "$CONFIG"; then
  say "automatic updates disabled"; exit 0
fi

install -d -m 755 "$STATE_DIR/bin"
LATEST="$(git ls-remote "$UPSTREAM_REPO" refs/heads/main | awk '{print $1}')"
[ -n "$LATEST" ] || { say "cannot resolve upstream main"; exit 1; }
CURRENT="$(cat "$STATE_DIR/upstream-version" 2>/dev/null || true)"
if [ "$LATEST" = "$CURRENT" ] && [ "$FORCE" -eq 0 ]; then say "already current ($LATEST)"; exit 0; fi

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
say "building upstream $LATEST"
git clone --quiet --filter=blob:none --no-checkout "$UPSTREAM_REPO" "$TMP/src"
git -C "$TMP/src" fetch --quiet --depth 1 origin "$LATEST"
git -C "$TMP/src" checkout --quiet --detach FETCH_HEAD
(cd "$TMP/src" && GOTOOLCHAIN=auto go build -trimpath -ldflags='-s -w' -o "$TMP/openflux" .)
"$TMP/openflux" --help >/dev/null 2>&1

if [ -f "$BINARY" ]; then cp -p "$BINARY" "$BINARY.rollback"; fi
install -m 755 "$TMP/openflux" "$BINARY.new"
mv "$BINARY.new" "$BINARY"
printf '%s\n' "$LATEST" >"$STATE_DIR/upstream-version"
say "installed $LATEST"

if [ -f /.dockerenv ]; then
  kill -HUP 1
elif command -v systemctl >/dev/null 2>&1; then
  systemctl kill -s HUP --kill-who=main openflux-panel.service
fi

# Verify that an enabled exit node stays alive with the new binary. The panel
# itself is not restarted, so this check also works when the update was started
# from the web UI. Restore the previous binary atomically on failure.
sleep 4
PORT="${OPENFLUX_LISTEN##*:}"
SCHEME=http
[ -n "${OPENFLUX_TLS_CERT:-}" ] && SCHEME=https
STATE="$(curl -kfsS --max-time 8 "$SCHEME://127.0.0.1:$PORT/healthz" 2>/dev/null || true)"
if ! printf '%s' "$STATE" | jq -e '.ok == true and (.running >= .enabled)' >/dev/null 2>&1; then
  if [ -f "$BINARY.rollback" ]; then
    say "new binary did not stay running; rolling back"
    cp -p "$BINARY.rollback" "$BINARY.new"
    mv "$BINARY.new" "$BINARY"
    printf '%s\n' "$CURRENT" >"$STATE_DIR/upstream-version"
    if [ -f /.dockerenv ]; then kill -HUP 1; else systemctl kill -s HUP --kill-who=main openflux-panel.service; fi
    exit 1
  fi
fi
say "health check passed"
