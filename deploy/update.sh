#!/usr/bin/env bash
set -Eeuo pipefail

P1_REPO="https://github.com/p1neappleXpress/OpenFlux.git"
TEAM_REPO="https://github.com/OpenFluxTeam/OpenFlux.git"
UPSTREAM_REPO="${OPENFLUX_UPSTREAM_REPO:-$P1_REPO}"
CONFIG="${OPENFLUX_CONFIG:-/etc/openflux-deploy/config.json}"
STATE_DIR="${OPENFLUX_STATE_DIR:-/var/lib/openflux-deploy}"
BINARY="${OPENFLUX_BINARY:-$STATE_DIR/bin/openflux}"
VERSION_FILE="$STATE_DIR/upstream-version"
LABEL_FILE="$STATE_DIR/upstream-version-label"
PATCH_FILE="$STATE_DIR/server-patch-revision"
PATCH_DIR="${OPENFLUX_PATCH_DIR:-/usr/local/lib/openflux-deploy}"
CORE_FILE="$STATE_DIR/core-line"
ENV_FILE="${OPENFLUX_ENV_FILE:-/etc/openflux-deploy/panel.env}"
DOTENV_FILE="${OPENFLUX_DOTENV_FILE:-/opt/openflux-deploy/.env}"
FORCE=0
TARGET_CORE=""
say(){ printf '[OpenFlux update] %s\n' "$*"; }
while [ "$#" -gt 0 ]; do
  case "$1" in
    --force) FORCE=1;;
    --core) TARGET_CORE="${2:?}"; shift;;
    --rollback) ROLLBACK=1;;
    *) say "unknown argument: $1"; exit 2;;
  esac
  shift
done

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
core_for_repo(){
  case "$1" in *OpenFluxTeam/OpenFlux*) printf '%s\n' team;; *) printf '%s\n' p1;; esac
}
repo_for_core(){
  case "$1" in team) printf '%s\n' "$TEAM_REPO";; p1) printf '%s\n' "$P1_REPO";; *) return 1;; esac
}
set_env_value(){
  local file key value escaped
  file="$1"; key="$2"; value="$3"
  [ -f "$file" ] || return 0
  escaped="${value//\\/\\\\}"; escaped="${escaped//&/\\&}"
  if grep -q "^${key}=" "$file"; then
    sed -i "s|^${key}=.*|${key}=${escaped}|" "$file"
  else
    printf '%s=%s\n' "$key" "$value" >>"$file"
  fi
}
persist_core(){
  local core repo
  core="$1"; repo="$(repo_for_core "$core")" || { say "invalid core: $core"; return 1; }
  set_env_value "$ENV_FILE" OPENFLUX_CORE_LINE "$core"
  set_env_value "$ENV_FILE" OPENFLUX_UPSTREAM_REPO "$repo"
  set_env_value "$DOTENV_FILE" OPENFLUX_CORE_LINE "$core"
  set_env_value "$DOTENV_FILE" OPENFLUX_UPSTREAM_REPO "$repo"
  printf '%s\n' "$core" >"$CORE_FILE.new" && mv "$CORE_FILE.new" "$CORE_FILE"
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
if [ "${ROLLBACK:-0}" = 1 ]; then
  [ -s "$BINARY.rollback" ] && [ -s "$VERSION_FILE.rollback" ] || { say 'no previous server version to restore'; exit 1; }
  "$BINARY.rollback" --help >/dev/null 2>&1 || { say 'previous server binary failed smoke test'; exit 1; }
  cp -p "$BINARY" "$BINARY.swap"
  cp -p "$BINARY.rollback" "$BINARY.new"
  cp -p "$VERSION_FILE" "$VERSION_FILE.swap"
  cp -p "$VERSION_FILE.rollback" "$VERSION_FILE.new"
  if [ -f "$LABEL_FILE" ]; then cp -p "$LABEL_FILE" "$LABEL_FILE.swap"; fi
  if [ -f "$LABEL_FILE.rollback" ]; then cp -p "$LABEL_FILE.rollback" "$LABEL_FILE.new"; fi
  mv "$BINARY.new" "$BINARY"
  mv "$BINARY.swap" "$BINARY.rollback"
  mv "$VERSION_FILE.new" "$VERSION_FILE"
  mv "$VERSION_FILE.swap" "$VERSION_FILE.rollback"
  rollback_failed_core="$(cat "$CORE_FILE" 2>/dev/null || core_for_repo "$UPSTREAM_REPO")"
  if [ -f "$CORE_FILE.rollback" ]; then
    rollback_core="$(tr -d '[:space:]' <"$CORE_FILE.rollback")"
    persist_core "$rollback_core"
    printf '%s\n' "$rollback_failed_core" >"$CORE_FILE.rollback"
  fi
  if [ -f "$LABEL_FILE.new" ]; then mv "$LABEL_FILE.new" "$LABEL_FILE"; fi
  if [ -f "$LABEL_FILE.swap" ]; then mv "$LABEL_FILE.swap" "$LABEL_FILE.rollback"; fi
  if [ -f "$PATCH_FILE.rollback" ]; then
    cp -p "$PATCH_FILE" "$PATCH_FILE.swap" 2>/dev/null || true
    cp -p "$PATCH_FILE.rollback" "$PATCH_FILE.new"
    mv "$PATCH_FILE.new" "$PATCH_FILE"
    if [ -f "$PATCH_FILE.swap" ]; then mv "$PATCH_FILE.swap" "$PATCH_FILE.rollback"; fi
  fi
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
    if [ -f "$PATCH_FILE.rollback" ]; then
      cp -p "$PATCH_FILE" "$PATCH_FILE.swap" 2>/dev/null || true
      cp -p "$PATCH_FILE.rollback" "$PATCH_FILE.new"
      mv "$PATCH_FILE.new" "$PATCH_FILE"
      if [ -f "$PATCH_FILE.swap" ]; then mv "$PATCH_FILE.swap" "$PATCH_FILE.rollback"; fi
    fi
    if [ -f "$CORE_FILE.rollback" ]; then
      persist_core "$rollback_failed_core"
      printf '%s\n' "$rollback_core" >"$CORE_FILE.rollback"
    fi
    restart_exit
    exit 1
  fi
  say 'rollback health check passed'
  exit 0
fi
if [ -n "$TARGET_CORE" ]; then
  case "$TARGET_CORE" in p1|team) ;; *) say 'core must be p1 or team'; exit 2;; esac
  UPSTREAM_REPO="$(repo_for_core "$TARGET_CORE")"
fi
CURRENT_CORE="${OPENFLUX_CORE_LINE:-}"
[ -n "$CURRENT_CORE" ] || CURRENT_CORE="$(cat "$CORE_FILE" 2>/dev/null || true)"
[ -n "$CURRENT_CORE" ] || CURRENT_CORE="$(core_for_repo "$UPSTREAM_REPO")"
[ -n "$TARGET_CORE" ] || UPSTREAM_REPO="$(repo_for_core "$CURRENT_CORE")"
if [ "$FORCE" -eq 0 ] && [ -f "$CONFIG" ] && grep -Eq '"auto_update"[[:space:]]*:[[:space:]]*false' "$CONFIG"; then
  say "automatic updates disabled"; exit 0
fi

install -d -m 755 "$STATE_DIR/bin"
# Upstream cuts stable node releases on release branches before (and sometimes
# without) merging them back into main. Follow the newest vX.Y.Z tag so the
# panel does not miss a released compatibility fix, such as the Mail.ru 0.7.1
# carrier change.
LATEST_TAG="$(git ls-remote --tags --refs "$UPSTREAM_REPO" 'refs/tags/v[0-9]*' | awk '{sub("refs/tags/", "", $2); print $2}' | grep -E '^v[0-9]+(\.[0-9]+)*$' | sort -V | tail -n 1)"
[ -n "$LATEST_TAG" ] || { say "cannot resolve an upstream release tag"; exit 1; }
TAG_REFS="$(git ls-remote "$UPSTREAM_REPO" "refs/tags/$LATEST_TAG^{}" "refs/tags/$LATEST_TAG")"
LATEST="$(printf '%s\n' "$TAG_REFS" | awk '/\^\{\}$/ { print $1; found=1; exit } END { if (!found && NR > 0) print $1 }')"
[ -n "$LATEST" ] || { say "cannot resolve upstream release $LATEST_TAG"; exit 1; }
CURRENT="$(cat "$VERSION_FILE" 2>/dev/null || true)"
PATCH_REVISION="$(sha256sum "$PATCH_DIR/patch-upstream.sh" "$PATCH_DIR"/patches/*.patch | sha256sum | awk '{print $1}')"
CURRENT_PATCH="$(cat "$PATCH_FILE" 2>/dev/null || true)"
if [ "$LATEST" = "$CURRENT" ] && [ "$PATCH_REVISION" = "$CURRENT_PATCH" ]; then say "already current ($LATEST_TAG · $LATEST)"; exit 0; fi

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
say "building upstream $LATEST_TAG ($LATEST)"
git clone --quiet --filter=blob:none --no-checkout "$UPSTREAM_REPO" "$TMP/src"
git -C "$TMP/src" fetch --quiet --depth 1 origin "$LATEST"
git -C "$TMP/src" checkout --quiet --detach FETCH_HEAD
UPSTREAM_LABEL="$(git -C "$TMP/src" describe --tags --always --match 'v[0-9]*') · $(git -C "$TMP/src" rev-parse --short=8 HEAD)"
sh /usr/local/lib/openflux-deploy/patch-upstream.sh "$TMP/src"
(cd "$TMP/src" && GOTOOLCHAIN=auto go build -trimpath -ldflags='-s -w' -o "$TMP/openflux" .)
"$TMP/openflux" --help >/dev/null 2>&1

if [ -f "$BINARY" ]; then cp -p "$BINARY" "$BINARY.rollback"; fi
if [ -f "$VERSION_FILE" ]; then cp -p "$VERSION_FILE" "$VERSION_FILE.rollback"; fi
if [ -f "$LABEL_FILE" ]; then cp -p "$LABEL_FILE" "$LABEL_FILE.rollback"; fi
if [ "$TARGET_CORE" != "" ] && [ "$TARGET_CORE" != "$CURRENT_CORE" ]; then
  printf '%s\n' "$CURRENT_CORE" >"$CORE_FILE.rollback"
  persist_core "$TARGET_CORE"
fi
printf '%s\n' "${CURRENT_PATCH:-legacy}" >"$PATCH_FILE.rollback"
install -m 755 "$TMP/openflux" "$BINARY.new"
mv "$BINARY.new" "$BINARY"
printf '%s\n' "$LATEST" >"$VERSION_FILE.new"
mv "$VERSION_FILE.new" "$VERSION_FILE"
printf '%s\n' "$UPSTREAM_LABEL" >"$LABEL_FILE.new"
mv "$LABEL_FILE.new" "$LABEL_FILE"
say "installed $UPSTREAM_LABEL"

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
    if [ -f "$LABEL_FILE.rollback" ]; then cp -p "$LABEL_FILE.rollback" "$LABEL_FILE.new" && mv "$LABEL_FILE.new" "$LABEL_FILE"; fi
    if [ "$TARGET_CORE" != "" ] && [ "$TARGET_CORE" != "$CURRENT_CORE" ]; then persist_core "$CURRENT_CORE"; fi
    restart_exit
    exit 1
  fi
  say 'new binary failed health check and no previous binary is available'
  exit 1
fi
printf '%s\n' "$PATCH_REVISION" >"$PATCH_FILE"
say "health check passed"
