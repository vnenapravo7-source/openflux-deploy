#!/usr/bin/env bash
set -Eeuo pipefail
STATE_DIR=/var/lib/openflux-deploy
install -d -m 755 "$STATE_DIR/bin"
if [ ! -x "$STATE_DIR/bin/openflux" ]; then
  install -m 755 /usr/local/lib/openflux-deploy/openflux.seed "$STATE_DIR/bin/openflux"
  cp /usr/local/lib/openflux-deploy/upstream-version.seed "$STATE_DIR/upstream-version"
fi
# The rule is isolated to the container network namespace and is needed only by L3.
iptables -C OUTPUT -p tcp --tcp-flags RST RST -j DROP 2>/dev/null || \
  iptables -A OUTPUT -p tcp --tcp-flags RST RST -j DROP
exec /usr/local/bin/openflux-panel
