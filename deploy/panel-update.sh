#!/usr/bin/env bash
set -Eeuo pipefail

REPO="${OPENFLUX_DEPLOY_REPO:-vnenapravo7-source/openflux-deploy}"
REF="${OPENFLUX_DEPLOY_REF:-main}"
STATE_DIR="${OPENFLUX_STATE_DIR:-/var/lib/openflux-deploy}"
PANEL_BIN="$STATE_DIR/bin/openflux-panel"
REVISION_FILE="$STATE_DIR/panel-revision"

say(){ printf '[OpenFlux panel update] %s\n' "$*"; }
[[ "$REPO" =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ ]] || { say 'invalid repository name'; exit 1; }
[[ "$REF" =~ ^[A-Za-z0-9_.-]+$ ]] || { say 'invalid branch name'; exit 1; }
[[ "$STATE_DIR" = /* && "$STATE_DIR" != / ]] || { say 'state directory must be an absolute non-root path'; exit 1; }

export GOCACHE="${GOCACHE:-$STATE_DIR/go-cache}"
export GOMODCACHE="${GOMODCACHE:-$STATE_DIR/go-mod}"
export GOPATH="${GOPATH:-$STATE_DIR/go-path}"
install -d -m 755 "$STATE_DIR/bin"

LATEST="$(git ls-remote "https://github.com/$REPO.git" "refs/heads/$REF" | awk '{print $1}')"
[ -n "$LATEST" ] || { say 'cannot resolve deploy branch'; exit 1; }
CURRENT="$(cat "$REVISION_FILE" 2>/dev/null || true)"
if [ "$LATEST" = "$CURRENT" ]; then say "already current ($LATEST)"; exit 0; fi

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
say "building panel from $REPO@$LATEST"
git clone --quiet --depth 1 --single-branch --branch "$REF" "https://github.com/$REPO.git" "$TMP/source"
CHECKED_OUT="$(git -C "$TMP/source" rev-parse HEAD)"
(cd "$TMP/source/deploy/panel" && GOTOOLCHAIN=auto CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o "$TMP/openflux-panel" .)
"$TMP/openflux-panel" --version | grep -q '^OpenFlux panel '

if [ -f "$PANEL_BIN" ]; then cp -p "$PANEL_BIN" "$PANEL_BIN.rollback"; fi
if [ -f "$REVISION_FILE" ]; then cp -p "$REVISION_FILE" "$REVISION_FILE.rollback"; fi
install -m 755 "$TMP/openflux-panel" "$PANEL_BIN.new"
mv "$PANEL_BIN.new" "$PANEL_BIN"
printf '%s\n' "$CHECKED_OUT" >"$REVISION_FILE"
say "installed $CHECKED_OUT; previous binary: $PANEL_BIN.rollback"

# Called by the panel API: terminate only the panel process that launched this
# updater. systemd Restart=always and Docker restart: unless-stopped bring it
# back using the newly installed binary. A manual script run does not kill its
# invoking shell.
PID="${OPENFLUX_PANEL_PID:-}"
if [[ "$PID" =~ ^[0-9]+$ ]] && [ "$PID" -gt 1 ]; then
  EXECUTABLE="$(readlink "/proc/$PID/exe" 2>/dev/null || true)"
  case "$EXECUTABLE" in
    */openflux-panel|*/openflux-panel\ \(deleted\)) say "restarting panel pid $PID"; kill -TERM "$PID";;
    *) say 'panel process changed; restart the service/container manually';;
  esac
else
  say 'restart the panel service/container to apply the new binary'
fi
