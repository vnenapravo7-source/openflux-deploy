#!/usr/bin/env bash
set -Eeuo pipefail

PREFIX=/opt/openflux-deploy
CONFIG_DIR=/etc/openflux-deploy
STATE_DIR=/var/lib/openflux-deploy
TOOLING_DIR=/usr/local/lib/openflux-deploy
PURGE_DATA=0
YES=0

say() { printf '[OpenFlux uninstall] %s\n' "$*"; }
fail() { say "ОШИБКА: $*" >&2; exit 1; }
usage() {
  cat <<'EOF'
Удаление OpenFlux Deploy с этого VPS (панель и управляемые ей процессы OpenFlux).
По умолчанию конфигурация и данные сохраняются.

Параметры:
  --purge-data   дополнительно удалить конфигурацию, пользователей, ключи,
                 сертификаты, логи, резервные копии и данные панели
  --yes          не задавать вопрос подтверждения
  -h, --help     показать эту справку
EOF
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --purge-data) PURGE_DATA=1; shift ;;
    --yes|-y) YES=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) fail "неизвестный параметр: $1" ;;
  esac
done

[ "$(id -u)" -eq 0 ] || fail "запустите удалятор от root (например, через sudo)"

# These paths are fixed by the installer. Refuse to follow a substituted
# symlink, especially when the user explicitly requested data deletion.
for path in "$PREFIX" "$TOOLING_DIR"; do
  [ ! -L "$path" ] || fail "$path является символьной ссылкой; остановился для безопасности"
done
if [ "$PURGE_DATA" -eq 1 ]; then
  for path in "$CONFIG_DIR" "$STATE_DIR"; do
    [ ! -L "$path" ] || fail "$path является символьной ссылкой; остановился для безопасности"
  done
fi

say "Будут остановлены панель и управляемые ею процессы OpenFlux."
if [ "$PURGE_DATA" -eq 1 ]; then
  say "Также будут удалены конфигурация и сохранённые данные."
else
  say "Конфигурация и данные останутся в $CONFIG_DIR и $STATE_DIR."
fi
say "Правила системного firewall и установленные Docker/Go пакеты не меняются."

if [ "$YES" -ne 1 ]; then
  if [ ! -r /dev/tty ]; then
    fail "нет интерактивного терминала для подтверждения; проверьте цель и повторите с --yes"
  fi
  if ! read -r -p "Продолжить удаление? [y/N] " answer </dev/tty; then answer=""; fi
  case "$answer" in y|Y|yes|YES) ;; *) say "Отменено."; exit 0 ;; esac
fi

if command -v systemctl >/dev/null 2>&1; then
  for unit in openflux-update.timer openflux-update.service openflux-panel.service; do
    systemctl disable --now "$unit" >/dev/null 2>&1 || true
  done
fi

if command -v docker >/dev/null 2>&1; then
  if [ -f "$PREFIX/docker-compose.yml" ]; then
    (cd "$PREFIX" && docker compose down --remove-orphans) >/dev/null 2>&1 || true
  fi
  if docker container inspect openflux-deploy >/dev/null 2>&1; then
    docker container rm --force openflux-deploy >/dev/null
  fi
  if docker image inspect openflux-deploy:local >/dev/null 2>&1; then
    docker image rm openflux-deploy:local >/dev/null 2>&1 || say "Образ openflux-deploy:local оставлен: возможно, он используется другим контейнером."
  fi
fi

if command -v systemctl >/dev/null 2>&1; then
  rm -f /etc/systemd/system/openflux-panel.service \
        /etc/systemd/system/openflux-update.service \
        /etc/systemd/system/openflux-update.timer
  systemctl daemon-reload >/dev/null 2>&1 || true
  systemctl reset-failed openflux-panel.service openflux-update.service openflux-update.timer >/dev/null 2>&1 || true
fi

rm -rf -- "$PREFIX" "$TOOLING_DIR"
if [ "$PURGE_DATA" -eq 1 ]; then
  rm -rf -- "$CONFIG_DIR" "$STATE_DIR"
fi

say "Удаление завершено."
if [ "$PURGE_DATA" -eq 1 ]; then
  say "Файлы приложения, конфигурация и данные OpenFlux Deploy удалены."
else
  say "Файлы приложения удалены; данные сохранены для возможной переустановки."
fi
