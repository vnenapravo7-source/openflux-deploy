#!/usr/bin/env bash
set -Eeuo pipefail

REPO="${OPENFLUX_DEPLOY_REPO:-vnenapravo7-source/openflux-deploy}"
REF="${OPENFLUX_DEPLOY_REF:-main}"
CORE_LINE="${OPENFLUX_CORE_LINE:-p1}"
CORE_EXPLICIT=0
UPSTREAM_REPO="${OPENFLUX_UPSTREAM_REPO:-}"
INSTALL_MODE="${OPENFLUX_INSTALL_MODE:-auto}"
ROLE="${OPENFLUX_ROLE:-controller}"
PORT="${OPENFLUX_PORT:-8088}"
PORT_EXPLICIT=0
ADMIN_USER="${OPENFLUX_ADMIN_USER:-admin}"
ADMIN_PASSWORD="${OPENFLUX_ADMIN_PASSWORD:-}"
NODE_TOKEN="${OPENFLUX_NODE_TOKEN:-}"
PREFIX=/opt/openflux-deploy
CONFIG_DIR=/etc/openflux-deploy
STATE_DIR=/var/lib/openflux-deploy

say(){ printf '\033[1;36m[OpenFlux]\033[0m %s\n' "$*"; }
fail(){ printf '\033[1;31m[OpenFlux] Ошибка:\033[0m %s\n' "$*" >&2; exit 1; }
need_root(){ [ "$(id -u)" -eq 0 ] || fail "запустите установщик от root: sudo bash ..."; }
have(){ command -v "$1" >/dev/null 2>&1; }

# The first address from `hostname -I` is frequently a Docker, VPN or private
# network address. Prefer the outward-facing IPv4 only for the user-facing
# installation hint; the panel itself still listens on every interface.
public_ipv4(){
  local ip
  ip="$(curl -4fsS --connect-timeout 3 --max-time 5 https://api.ipify.org 2>/dev/null || true)"
  case "$ip" in
    *[!0-9.]*|*.*.*.*.*|'' ) return 1 ;;
  esac
  printf '%s\n' "$ip"
}
random_hex(){ od -An -N "$1" -tx1 /dev/urandom | tr -d ' \n'; }
escape_env(){ local v="$1"; v="${v//\\/\\\\}"; v="${v//\"/\\\"}"; printf '"%s"' "$v"; }

usage(){ cat <<'EOF'
OpenFlux Deploy
  --mode systemd|docker   способ установки
  --role controller|node standalone/центральная панель или подключаемая нода
  --port PORT             HTTPS-порт панели (по умолчанию 8088)
  --core p1|team          ядро p1neappleXpress (по умолчанию) или OpenFluxTeam

Переменные: OPENFLUX_ADMIN_USER, OPENFLUX_ADMIN_PASSWORD, OPENFLUX_NODE_TOKEN,
OPENFLUX_INSTALL_MODE, OPENFLUX_ROLE, OPENFLUX_PORT, OPENFLUX_CORE_LINE.
EOF
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --mode) INSTALL_MODE="${2:?}"; shift 2;;
    --role) ROLE="${2:?}"; shift 2;;
    --port) PORT="${2:?}"; PORT_EXPLICIT=1; shift 2;;
    --core) CORE_LINE="${2:?}"; CORE_EXPLICIT=1; shift 2;;
    -h|--help) usage; exit 0;;
    *) fail "неизвестный аргумент: $1";;
  esac
done

need_root
case "$ROLE" in controller|node) ;; *) fail "--role: controller или node";; esac
case "$CORE_LINE" in p1|team) ;; *) fail "--core: p1 или team";; esac
case "$PORT" in *[!0-9]*|'') fail "порт должен быть числом";; esac
[ "$PORT" -ge 1 ] && [ "$PORT" -le 65535 ] || fail "порт вне диапазона"

# A reinstall must keep the selected compatibility line even when the shell
# itself has no OPENFLUX_CORE_LINE environment variable.
if [ "$CORE_EXPLICIT" -eq 0 ] && [ -f "$CONFIG_DIR/panel.env" ]; then
  SAVED_CORE_LINE="$(sed -n 's/^OPENFLUX_CORE_LINE=//p' "$CONFIG_DIR/panel.env" | head -n 1 | tr -d '\"')"
  case "$SAVED_CORE_LINE" in p1|team) CORE_LINE="$SAVED_CORE_LINE";; esac
fi
case "$CORE_LINE" in
  p1) DEFAULT_UPSTREAM_REPO="https://github.com/p1neappleXpress/OpenFlux.git";;
  team) DEFAULT_UPSTREAM_REPO="https://github.com/OpenFluxTeam/OpenFlux.git";;
esac
UPSTREAM_REPO="${UPSTREAM_REPO:-$DEFAULT_UPSTREAM_REPO}"

if [ "$INSTALL_MODE" = auto ]; then
  if [ -f "$CONFIG_DIR/install-mode" ]; then
    INSTALL_MODE="$(tr -d '[:space:]' <"$CONFIG_DIR/install-mode")"
  fi
fi
if [ "$INSTALL_MODE" = auto ]; then
  if have docker && docker compose version >/dev/null 2>&1; then INSTALL_MODE=docker; else INSTALL_MODE=systemd; fi
fi
case "$INSTALL_MODE" in systemd|docker) ;; *) fail "--mode: systemd или docker";; esac

EXISTING_USERS=0
if [ -f "$CONFIG_DIR/users.json" ]; then
  EXISTING_USERS=1
  [ -f "$CONFIG_DIR/panel.env" ] || fail "users.json существует, но panel.env отсутствует; восстановите конфигурацию перед переустановкой"
  if [ "$CORE_EXPLICIT" -eq 1 ]; then
    OLD_CORE_LINE="$(sed -n 's/^OPENFLUX_CORE_LINE=//p' "$CONFIG_DIR/panel.env" | head -n 1 | tr -d '\"')"
    if [ -n "$OLD_CORE_LINE" ] && [ "$OLD_CORE_LINE" != "$CORE_LINE" ]; then
      fail "смена ядра с $OLD_CORE_LINE на $CORE_LINE не выполняется поверх существующей установки; используйте отдельный VPS, чтобы не нарушить совместимость подключений"
    fi
  fi
  if [ "$PORT_EXPLICIT" -eq 0 ]; then
    OLD_PORT="$(sed -n 's/^OPENFLUX_LISTEN=://p' "$CONFIG_DIR/panel.env" | head -n 1)"
    [ -n "$OLD_PORT" ] && PORT="$OLD_PORT"
    say "Найдены существующие пользователи: пароль и порт останутся прежними"
  else
    say "Найдены существующие пользователи: пароль сохранится, порт будет изменён на $PORT"
  fi
else
  if [ -z "$ADMIN_PASSWORD" ]; then ADMIN_PASSWORD="$(random_hex 12)"; GENERATED_PASSWORD=1; else GENERATED_PASSWORD=0; fi
  if [ -z "$NODE_TOKEN" ]; then NODE_TOKEN="$(random_hex 32)"; fi
fi
GENERATED_PASSWORD="${GENERATED_PASSWORD:-0}"
case "$ADMIN_USER$ADMIN_PASSWORD$NODE_TOKEN" in *$'\n'*|*$'\r'*) fail "логин, пароль и токен не должны содержать переносы строк";; esac

install_packages(){
  if have apt-get; then
    apt-get update -qq
    DEBIAN_FRONTEND=noninteractive apt-get install -y -qq ca-certificates curl git tar openssl python3 iptables jq >/dev/null
  elif have dnf; then
    dnf install -y ca-certificates curl git tar openssl python3 iptables jq >/dev/null
  elif have apk; then
    apk add --no-cache ca-certificates curl git tar openssl python3 iptables jq >/dev/null
  else fail "поддерживаются apt, dnf и apk"; fi
}

fetch_source(){
  local tmp archive archive_ref
  tmp="$(mktemp -d)"; archive="$tmp/source.tgz"
  DEPLOY_REVISION="$(git ls-remote "https://github.com/${REPO}.git" "refs/heads/${REF}" 2>/dev/null | awk '{print $1}' || true)"
  archive_ref="${DEPLOY_REVISION:-refs/heads/${REF}}"
  curl -fsSL --retry 3 "https://codeload.github.com/${REPO}/tar.gz/${archive_ref}" -o "$archive"
  tar -xzf "$archive" -C "$tmp"
  rm -rf "$PREFIX/source.new"
  mv "$tmp"/*/ "$PREFIX/source.new"
  if [ -d "$PREFIX/source" ]; then rm -rf "$PREFIX/source.previous"; mv "$PREFIX/source" "$PREFIX/source.previous"; fi
  mv "$PREFIX/source.new" "$PREFIX/source"
  rm -rf "$tmp"
}

resolve_upstream_release(){
  UPSTREAM_TAG="$(git ls-remote --tags --refs "$UPSTREAM_REPO" 'refs/tags/v[0-9]*' | awk '{sub("refs/tags/", "", $2); print $2}' | grep -E '^v[0-9]+(\.[0-9]+)*$' | sort -V | tail -n 1)"
  [ -n "$UPSTREAM_TAG" ] || fail "не удалось получить стабильный релиз OpenFlux"
  UPSTREAM_REVISION="$(git ls-remote "$UPSTREAM_REPO" "refs/tags/$UPSTREAM_TAG^{}" "refs/tags/$UPSTREAM_TAG" | awk '/\^\{\}$/ { print $1; found=1; exit } END { if (!found && NR > 0) print $1 }')"
  [ -n "$UPSTREAM_REVISION" ] || fail "не удалось получить ревизию OpenFlux $UPSTREAM_TAG"
}

make_config(){
  install -d -m 700 "$CONFIG_DIR" "$STATE_DIR" "$CONFIG_DIR/tls"
  if [ ! -f "$CONFIG_DIR/config.json" ]; then
    cat >"$CONFIG_DIR/config.json" <<'JSON'
{
  "enabled": false,
  "transport": "yandex",
  "url": "",
  "mode": "l4",
  "codec": "batched",
  "local_ip": "",
  "encryption_key_file": "",
  "debug": false,
  "auto_update": true
}
JSON
    chmod 600 "$CONFIG_DIR/config.json"
  fi
  [ -f "$CONFIG_DIR/nodes.json" ] || { printf '[]\n' >"$CONFIG_DIR/nodes.json"; chmod 600 "$CONFIG_DIR/nodes.json"; }
  if [ ! -s "$CONFIG_DIR/tls/cert.pem" ] || [ ! -s "$CONFIG_DIR/tls/key.pem" ]; then
    local host ip san
    host="$(hostname -f 2>/dev/null || hostname)"; ip="$(hostname -I 2>/dev/null | awk '{print $1}')"
    san="DNS:${host}"; [ -n "$ip" ] && san="$san,IP:$ip"
    openssl req -x509 -newkey rsa:3072 -sha256 -nodes -days 825 \
      -subj "/CN=$host" -addext "subjectAltName=$san" \
      -keyout "$CONFIG_DIR/tls/key.pem" -out "$CONFIG_DIR/tls/cert.pem" >/dev/null 2>&1
    chmod 600 "$CONFIG_DIR/tls/key.pem"
  fi
}

install_go(){
  local version arch json sha file tmp
  version="$(awk '/^go /{print $2;exit}' "${1:-$PREFIX/source}/go.mod")"
  arch="$(uname -m)"; case "$arch" in x86_64) arch=amd64;; aarch64|arm64) arch=arm64;; *) fail "архитектура $arch не поддерживается";; esac
  if have go && go version 2>/dev/null | grep -q "go${version}"; then return; fi
  say "Устанавливаю Go $version с проверкой SHA-256"
  tmp="$(mktemp -d)"; file="go${version}.linux-${arch}.tar.gz"
  json="$(curl -fsSL 'https://go.dev/dl/?mode=json&include=all')"
  sha="$(printf '%s' "$json" | python3 -c 'import json,sys; d=json.load(sys.stdin); v=sys.argv[1]; print(next(f["sha256"] for r in d if r["version"]=="go"+v for f in r["files"] if f["filename"]=="go"+v+".linux-"+sys.argv[2]+".tar.gz"))' "$version" "$arch")"
  curl -fsSL --retry 3 "https://go.dev/dl/$file" -o "$tmp/$file"
  printf '%s  %s\n' "$sha" "$tmp/$file" | sha256sum -c - >/dev/null
  rm -rf /usr/local/go; tar -C /usr/local -xzf "$tmp/$file"; ln -sf /usr/local/go/bin/go /usr/local/bin/go
  rm -rf "$tmp"
}

write_env(){
  if [ "$EXISTING_USERS" -ne 0 ]; then
    # Keep credentials and the node token, but honor an explicitly requested
    # listening port during a reinstall.
    if [ "$PORT_EXPLICIT" -eq 1 ]; then
      if grep -q '^OPENFLUX_LISTEN=' "$CONFIG_DIR/panel.env"; then
        sed -i "s|^OPENFLUX_LISTEN=.*|OPENFLUX_LISTEN=:$PORT|" "$CONFIG_DIR/panel.env"
      else
        printf 'OPENFLUX_LISTEN=:%s\n' "$PORT" >>"$CONFIG_DIR/panel.env"
      fi
    fi
    return 0
  fi
  umask 077
  cat >"$CONFIG_DIR/panel.env" <<EOF
OPENFLUX_ADMIN_USER=$(escape_env "$ADMIN_USER")
OPENFLUX_ADMIN_PASSWORD=$(escape_env "$ADMIN_PASSWORD")
OPENFLUX_NODE_TOKEN=$(escape_env "$NODE_TOKEN")
OPENFLUX_LISTEN=:$PORT
OPENFLUX_CONFIG=$CONFIG_DIR/config.json
OPENFLUX_USERS=$CONFIG_DIR/users.json
OPENFLUX_CONNECTIONS=$CONFIG_DIR/connections.json
OPENFLUX_NODES=$CONFIG_DIR/nodes.json
OPENFLUX_INSTRUCTIONS=$CONFIG_DIR/instructions.json
OPENFLUX_INSTRUCTION_ASSETS=$STATE_DIR/instruction-assets
OPENFLUX_TLS_CERT=$CONFIG_DIR/tls/cert.pem
OPENFLUX_TLS_KEY=$CONFIG_DIR/tls/key.pem
OPENFLUX_VERSION_FILE=$STATE_DIR/upstream-version
OPENFLUX_UPDATE_SCRIPT=/usr/local/lib/openflux-deploy/update.sh
OPENFLUX_PANEL_UPDATE_SCRIPT=/usr/local/lib/openflux-deploy/panel-update.sh
OPENFLUX_PANEL_REVISION_FILE=$STATE_DIR/panel-revision
OPENFLUX_ROLLBACK_CAPABLE=1
OPENFLUX_DEPLOY_REPO=$(escape_env "$REPO")
OPENFLUX_DEPLOY_REF=$(escape_env "$REF")
OPENFLUX_CORE_LINE=$(escape_env "$CORE_LINE")
OPENFLUX_UPSTREAM_REPO=$(escape_env "$UPSTREAM_REPO")
OPENFLUX_BINARY=$STATE_DIR/bin/openflux
EOF
}

install_systemd(){
  [ -d /run/systemd/system ] || fail "systemd не найден; используйте --mode docker"
  local upstream_tmp upstream_src
  upstream_tmp="$(mktemp -d)"; upstream_src="$upstream_tmp/source"
  git clone --quiet --depth 1 --single-branch --branch "$UPSTREAM_TAG" "$UPSTREAM_REPO" "$upstream_src"
  sh "$PREFIX/source/deploy/patch-upstream.sh" "$upstream_src"
  install_go "$upstream_src"
  say "Собираю OpenFlux и панель"
  install -d -m 755 "$STATE_DIR/bin" /usr/local/lib/openflux-deploy
  (cd "$upstream_src" && GOTOOLCHAIN=auto go build -trimpath -ldflags='-s -w' -o "$STATE_DIR/bin/openflux.new" .)
  (cd "$PREFIX/source/deploy/panel" && GOTOOLCHAIN=auto go build -trimpath -ldflags='-s -w' -o "$STATE_DIR/bin/openflux-panel.new" .)
  chmod 755 "$STATE_DIR/bin/openflux.new" "$STATE_DIR/bin/openflux-panel.new"
  if [ -f "$STATE_DIR/bin/openflux" ]; then cp -p "$STATE_DIR/bin/openflux" "$STATE_DIR/bin/openflux.rollback"; fi
  if [ -f "$STATE_DIR/upstream-version" ]; then cp -p "$STATE_DIR/upstream-version" "$STATE_DIR/upstream-version.rollback"; fi
  mv "$STATE_DIR/bin/openflux.new" "$STATE_DIR/bin/openflux"
  if [ -f "$STATE_DIR/bin/openflux-panel" ]; then cp -p "$STATE_DIR/bin/openflux-panel" "$STATE_DIR/bin/openflux-panel.rollback"; fi
  if [ -f "$STATE_DIR/panel-revision" ]; then cp -p "$STATE_DIR/panel-revision" "$STATE_DIR/panel-revision.rollback"; fi
  mv "$STATE_DIR/bin/openflux-panel.new" "$STATE_DIR/bin/openflux-panel"
  printf '%s\n' "${DEPLOY_REVISION:-bundled}" >"$STATE_DIR/panel-revision"
  git -C "$upstream_src" rev-parse HEAD >"$STATE_DIR/upstream-version"
  printf '%s · %s\n' "$UPSTREAM_TAG" "$(git -C "$upstream_src" rev-parse --short=8 HEAD)" >"$STATE_DIR/upstream-version-label"
  rm -rf "$upstream_tmp"
  install -m 755 "$PREFIX/source/deploy/update.sh" /usr/local/lib/openflux-deploy/update.sh
  install -m 755 "$PREFIX/source/deploy/patch-upstream.sh" /usr/local/lib/openflux-deploy/patch-upstream.sh
  install -d /usr/local/lib/openflux-deploy/patches
  install -m 644 "$PREFIX/source/deploy/patches/"*.patch /usr/local/lib/openflux-deploy/patches/
  sha256sum /usr/local/lib/openflux-deploy/patch-upstream.sh /usr/local/lib/openflux-deploy/patches/*.patch | sha256sum | awk '{print $1}' >"$STATE_DIR/server-patch-revision"
  install -m 755 "$PREFIX/source/deploy/panel-update.sh" /usr/local/lib/openflux-deploy/panel-update.sh
  printf 'systemd\n' >"$CONFIG_DIR/install-mode"
  cat >/etc/systemd/system/openflux-panel.service <<EOF
[Unit]
Description=OpenFlux exit node and control panel
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
EnvironmentFile=$CONFIG_DIR/panel.env
ExecStart=$STATE_DIR/bin/openflux-panel
Restart=always
RestartSec=3
NoNewPrivileges=false
PrivateTmp=true
ProtectHome=true
ProtectSystem=full
ReadWritePaths=$CONFIG_DIR $STATE_DIR
LimitNOFILE=1048576

[Install]
WantedBy=multi-user.target
EOF
  cat >/etc/systemd/system/openflux-update.service <<'EOF'
[Unit]
Description=Update OpenFlux from upstream
After=network-online.target

[Service]
Type=oneshot
EnvironmentFile=/etc/openflux-deploy/panel.env
ExecStart=/usr/local/lib/openflux-deploy/update.sh
EOF
  cat >/etc/systemd/system/openflux-update.timer <<'EOF'
[Unit]
Description=Daily OpenFlux upstream update check

[Timer]
OnBootSec=2min
OnUnitActiveSec=6h
RandomizedDelaySec=5min
Persistent=true

[Install]
WantedBy=timers.target
EOF
  systemctl daemon-reload
  systemctl enable openflux-panel.service openflux-update.timer
  systemctl restart openflux-panel.service
  systemctl start openflux-update.timer
}

install_docker(){
  have docker || fail "Docker не установлен"
  docker compose version >/dev/null 2>&1 || fail "Docker Compose v2 не установлен"
  say "Собираю Docker-образ"
  cp "$PREFIX/source/deploy/docker-compose.yml" "$PREFIX/docker-compose.yml"
  cp "$PREFIX/source/deploy/Dockerfile" "$PREFIX/Dockerfile"
  cp "$PREFIX/source/deploy/entrypoint.sh" "$PREFIX/entrypoint.sh"
  cp "$PREFIX/source/deploy/update.sh" "$PREFIX/update.sh"
  cp "$PREFIX/source/deploy/panel-update.sh" "$PREFIX/panel-update.sh"
  cp "$PREFIX/source/deploy/patch-upstream.sh" "$PREFIX/patch-upstream.sh"
  install -d "$PREFIX/patches"
  cp "$PREFIX/source/deploy/patches/"*.patch "$PREFIX/patches/"
  if [ "$EXISTING_USERS" -eq 0 ]; then cat >"$PREFIX/.env" <<EOF
OPENFLUX_PORT=$PORT
OPENFLUX_ADMIN_USER=$(escape_env "$ADMIN_USER")
OPENFLUX_ADMIN_PASSWORD=$(escape_env "$ADMIN_PASSWORD")
OPENFLUX_NODE_TOKEN=$(escape_env "$NODE_TOKEN")
OPENFLUX_UPSTREAM_REPO=$(escape_env "$UPSTREAM_REPO")
OPENFLUX_CORE_LINE=$(escape_env "$CORE_LINE")
OPENFLUX_DEPLOY_REPO=$(escape_env "$REPO")
OPENFLUX_DEPLOY_REF=$(escape_env "$REF")
EOF
    chmod 600 "$PREFIX/.env"
  else
    [ -f "$PREFIX/.env" ] || fail "существующая Docker-установка без .env; восстановите файл перед переустановкой"
  fi
  printf 'docker\n' >"$CONFIG_DIR/install-mode"
  export OPENFLUX_DEPLOY_REVISION="${DEPLOY_REVISION:-bundled}"
  export OPENFLUX_UPSTREAM_REVISION="$UPSTREAM_REVISION"
  (cd "$PREFIX" && docker compose build)
  touch "$STATE_DIR/panel-seed-next-start"
  touch "$STATE_DIR/server-seed-next-start"
  (cd "$PREFIX" && docker compose up -d --no-build --force-recreate)
}

install_packages
install -d -m 755 "$PREFIX"
fetch_source
resolve_upstream_release
make_config
write_env
if [ "$INSTALL_MODE" = systemd ]; then install_systemd; else install_docker; fi

FINGERPRINT="$(openssl x509 -in "$CONFIG_DIR/tls/cert.pem" -noout -fingerprint -sha256 | cut -d= -f2 | tr -d ':')"
IP="$(public_ipv4 || true)"
if [ -z "$IP" ]; then
  IP="$(hostname -I 2>/dev/null | awk '{print $1}')"
  [ -n "$IP" ] || IP="SERVER_IP"
  say "Не удалось определить публичный IP. Если это VPS, используйте его внешний IP или домен, а не $IP."
fi
say "Готово: https://$IP:$PORT"
if [ "$EXISTING_USERS" -eq 0 ]; then
  printf '  Логин: %s\n  Пароль: %s\n' "$ADMIN_USER" "$ADMIN_PASSWORD"
else
  say "Используйте прежние учётные данные. Пароль можно сменить в разделе «Пользователи»."
fi
if [ "$ROLE" = node ]; then
  printf '\n  Данные для подключения ноды:\n  URL: https://%s:%s\n  SHA-256: %s\n' "$IP" "$PORT" "$FINGERPRINT"
  if [ "$EXISTING_USERS" -eq 0 ]; then printf '  Токен: %s\n' "$NODE_TOKEN"; else say "Токен ноды сохранён в прежней конфигурации."; fi
fi
[ "$GENERATED_PASSWORD" -eq 1 ] && say "Сохраните сгенерированный пароль: повторно он не показывается."
say "Первый вход вызовет предупреждение о self-signed сертификате — сверьте SHA-256: $FINGERPRINT"
