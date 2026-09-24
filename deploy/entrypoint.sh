#!/usr/bin/env bash
set -Eeuo pipefail
STATE_DIR=/var/lib/openflux-deploy
install -d -m 755 "$STATE_DIR/bin"
if [ ! -x "$STATE_DIR/bin/openflux" ]; then
  install -m 755 /usr/local/lib/openflux-deploy/openflux.seed "$STATE_DIR/bin/openflux"
  cp /usr/local/lib/openflux-deploy/upstream-version.seed "$STATE_DIR/upstream-version"
fi
if [ ! -x "$STATE_DIR/bin/openflux-panel" ] || [ -f "$STATE_DIR/panel-seed-next-start" ]; then
  if [ -f "$STATE_DIR/bin/openflux-panel" ]; then cp -p "$STATE_DIR/bin/openflux-panel" "$STATE_DIR/bin/openflux-panel.rollback"; fi
  install -m 755 /usr/local/lib/openflux-deploy/openflux-panel.seed "$STATE_DIR/bin/openflux-panel.new"
  mv "$STATE_DIR/bin/openflux-panel.new" "$STATE_DIR/bin/openflux-panel"
  cp /usr/local/lib/openflux-deploy/panel-revision.seed "$STATE_DIR/panel-revision"
  rm -f "$STATE_DIR/panel-seed-next-start"
fi
# The rule is isolated to the container network namespace and is needed only by L3.
iptables -C OUTPUT -p tcp --tcp-flags RST RST -j DROP 2>/dev/null || \
  iptables -A OUTPUT -p tcp --tcp-flags RST RST -j DROP
exec "$STATE_DIR/bin/openflux-panel"
