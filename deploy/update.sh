#!/usr/bin/env bash
set -Eeuo pipefail

UPSTREAM_REPO="${OPENFLUX_UPSTREAM_REPO:-https://github.com/p1neappleXpress/OpenFlux.git}"
CONFIG="${OPENFLUX_CONFIG:-/etc/openflux-deploy/config.json}"
STATE_DIR="${OPENFLUX_STATE_DIR:-/var/lib/openflux-deploy}"
BINARY="${OPENFLUX_BINARY:-$STATE_DIR/bin/openflux}"
VERSION_FILE="$STATE_DIR/upstream-version"
FORCE=0
[ "${1:-}" = "--force" ] && FORCE=1

say(){ printf '[OpenFlux update] %s\n' "$*"; }
export GOCACHE="${GOCACHE:-$STATE_DIR/go-cache}"
export GOMODCACHE="${GOMODCACHE:-$STATE_DIR/go-mod}"
export GOPATH="${GOPATH:-$STATE_DIR/go-path}"
restart_exit(){
  if [ -f /.dockerenv ]; then
    kill -HUP 1
  elif command -v systemctl >/dev/null 2>&1; then
    systemctl kill -s HUP --kill-who=main openflux-panel.service
  fi
}
check_health(){
  local port scheme state attempt
  port="${OPENFLUX_LISTEN##*:}"
  scheme=http
  [ -n "${OPENFLUX_TLS_CERT:-}" ] && scheme=https
  for ((attempt=0; attempt<15; attempt++)); do
    sleep 2
    state="$(curl -kfsS --max-time 8 "$scheme://127.0.0.1:$port/healthz" 2>/dev/null || true)"
    if printf '%s' "$state" | jq -e '.ok == true and (.running >= .enabled)' >/dev/null 2>&1; then
      return 0
    fi
  done
  return 1
}
if [ "${1:-}" = "--rollback" ]; then
  [ -s "$BINARY.rollback" ] && [ -s "$VERSION_FILE.rollback" ] || { say 'no previous server version to restore'; exit 1; }
  "$BINARY.rollback" --help >/dev/null 2>&1 || { say 'previous server binary failed smoke test'; exit 1; }
  cp -p "$BINARY" "$BINARY.swap"
  cp -p "$BINARY.rollback" "$BINARY.new"
  cp -p "$VERSION_FILE" "$VERSION_FILE.swap"
  cp -p "$VERSION_FILE.rollback" "$VERSION_FILE.new"
  mv "$BINARY.new" "$BINARY"
  mv "$BINARY.swap" "$BINARY.rollback"
  mv "$VERSION_FILE.new" "$VERSION_FILE"
  mv "$VERSION_FILE.swap" "$VERSION_FILE.rollback"
  say "restored $(cat "$VERSION_FILE")"
  restart_exit
  if ! check_health; then
    say 'restored version failed health check; switching back'
    cp -p "$BINARY" "$BINARY.swap"
    cp -p "$BINARY.rollback" "$BINARY.new"
    cp -p "$VERSION_FILE" "$VERSION_FILE.swap"
    cp -p "$VERSION_FILE.rollback" "$VERSION_FILE.new"
    mv "$BINARY.new" "$BINARY"
    mv "$BINARY.swap" "$BINARY.rollback"
    mv "$VERSION_FILE.new" "$VERSION_FILE"
    mv "$VERSION_FILE.swap" "$VERSION_FILE.rollback"
    restart_exit
    exit 1
  fi
  say 'rollback health check passed'
  exit 0
fi
if [ "$FORCE" -eq 0 ] && [ -f "$CONFIG" ] && grep -Eq '"auto_update"[[:space:]]*:[[:space:]]*false' "$CONFIG"; then
  say "automatic updates disabled"; exit 0
fi

install -d -m 755 "$STATE_DIR/bin"
LATEST="$(git ls-remote "$UPSTREAM_REPO" refs/heads/main | awk '{print $1}')"
[ -n "$LATEST" ] || { say "cannot resolve upstream main"; exit 1; }
CURRENT="$(cat "$VERSION_FILE" 2>/dev/null || true)"
if [ "$LATEST" = "$CURRENT" ]; then say "already current ($LATEST)"; exit 0; fi

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
say "building upstream $LATEST"
git clone --quiet --filter=blob:none --no-checkout "$UPSTREAM_REPO" "$TMP/src"
git -C "$TMP/src" fetch --quiet --depth 1 origin "$LATEST"
git -C "$TMP/src" checkout --quiet --detach FETCH_HEAD
sh /usr/local/lib/openflux-deploy/patch-upstream.sh "$TMP/src"
(cd "$TMP/src" && GOTOOLCHAIN=auto go build -trimpath -ldflags='-s -w' -o "$TMP/openflux" .)
"$TMP/openflux" --help >/dev/null 2>&1

if [ -f "$BINARY" ]; then cp -p "$BINARY" "$BINARY.rollback"; fi
if [ -f "$VERSION_FILE" ]; then cp -p "$VERSION_FILE" "$VERSION_FILE.rollback"; fi
install -m 755 "$TMP/openflux" "$BINARY.new"
mv "$BINARY.new" "$BINARY"
printf '%s\n' "$LATEST" >"$VERSION_FILE.new"
mv "$VERSION_FILE.new" "$VERSION_FILE"
say "installed $LATEST"

restart_exit

# Verify that an enabled exit node stays alive with the new binary. The panel
# itself is not restarted, so this check also works when the update was started
# from the web UI. Restore the previous binary atomically on failure.
if ! check_health; then
  if [ -f "$BINARY.rollback" ]; then
    say "new binary did not stay running; rolling back"
    cp -p "$BINARY.rollback" "$BINARY.new"
    mv "$BINARY.new" "$BINARY"
    printf '%s\n' "$CURRENT" >"$VERSION_FILE.new"
    mv "$VERSION_FILE.new" "$VERSION_FILE"
    restart_exit
    exit 1
  fi
fi
say "health check passed"
