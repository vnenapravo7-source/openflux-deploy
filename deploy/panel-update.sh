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
command -v timeout >/dev/null || { say 'timeout command is required'; exit 1; }
restart_panel(){
  # Only terminate the panel process that launched this updater. The service
  # manager or Docker restart policy starts the selected binary again.
  local pid executable
  pid="${OPENFLUX_PANEL_PID:-}"
  if [[ "$pid" =~ ^[0-9]+$ ]] && [ "$pid" -ge 1 ]; then
    executable="$(readlink "/proc/$pid/exe" 2>/dev/null || true)"
    case "$executable" in
      */openflux-panel|*/openflux-panel\ \(deleted\))
        say "restarting panel pid $pid"
        kill -TERM "$pid"
        if [ "$pid" -eq 1 ] && [ -f /.dockerenv ]; then
          sleep 2
          if kill -0 1 2>/dev/null; then
            say 'PID 1 ignored SIGTERM; stopping container to trigger Docker restart'
            kill -KILL 1
          fi
        fi
        ;;
      *) say 'panel process changed; restart the service/container manually'; exit 1;;
    esac
  else
    say 'restart the panel service/container to apply the selected binary'
  fi
}
if [ "${1:-}" = "--rollback" ]; then
  [ -s "$PANEL_BIN.rollback" ] && [ -s "$REVISION_FILE.rollback" ] && [ -s "$PANEL_BIN" ] && [ -s "$REVISION_FILE" ] || { say 'no previous panel version to restore'; exit 1; }
  "$PANEL_BIN.rollback" --version | grep -q '^OpenFlux panel ' || { say 'previous panel binary failed smoke test'; exit 1; }
  cp -p "$PANEL_BIN" "$PANEL_BIN.swap"
  cp -p "$PANEL_BIN.rollback" "$PANEL_BIN.new"
  cp -p "$REVISION_FILE" "$REVISION_FILE.swap"
  cp -p "$REVISION_FILE.rollback" "$REVISION_FILE.new"
  mv "$PANEL_BIN.new" "$PANEL_BIN"
  mv "$PANEL_BIN.swap" "$PANEL_BIN.rollback"
  mv "$REVISION_FILE.new" "$REVISION_FILE"
  mv "$REVISION_FILE.swap" "$REVISION_FILE.rollback"
  say "restored $(cat "$REVISION_FILE")"
  restart_panel
  exit 0
fi

say 'checking the latest panel revision'
LATEST="$(timeout 2m git ls-remote "https://github.com/$REPO.git" "refs/heads/$REF" | awk '{print $1}')"
[ -n "$LATEST" ] || { say 'cannot resolve deploy branch'; exit 1; }
CURRENT="$(cat "$REVISION_FILE" 2>/dev/null || true)"
if [ "$LATEST" = "$CURRENT" ] && [ "${1:-}" != "--force" ]; then say "already current ($LATEST)"; exit 0; fi

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
say "downloading panel source from $REPO@$LATEST"
timeout 3m git clone --depth 1 --single-branch --branch "$REF" "https://github.com/$REPO.git" "$TMP/source"
CHECKED_OUT="$(git -C "$TMP/source" rev-parse HEAD)"
say 'building panel (up to 10 minutes)'
(cd "$TMP/source/deploy/panel" && timeout 10m env GOTOOLCHAIN=auto CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o "$TMP/openflux-panel" .)
say 'checking the new binary'
"$TMP/openflux-panel" --version | grep -q '^OpenFlux panel '

if [ -f "$PANEL_BIN" ]; then cp -p "$PANEL_BIN" "$PANEL_BIN.rollback"; fi
if [ -f "$REVISION_FILE" ]; then cp -p "$REVISION_FILE" "$REVISION_FILE.rollback"; fi
install -m 755 "$TMP/openflux-panel" "$PANEL_BIN.new"
mv "$PANEL_BIN.new" "$PANEL_BIN"
printf '%s\n' "$CHECKED_OUT" >"$REVISION_FILE.new"
mv "$REVISION_FILE.new" "$REVISION_FILE"
say "installed $CHECKED_OUT; previous binary: $PANEL_BIN.rollback"

restart_panel
