#!/bin/sh
# Gateway-only entrypoint: fix bind-mount ownership, create a config on first
# run, then drop privileges and exec wxcat.
set -eu

: "${WXCAT_CONFIG:=/app/config/wxcat.json}"
: "${WXCAT_DATA:=/app/data}"

mkdir -p "$(dirname "$WXCAT_CONFIG")" "$WXCAT_DATA" 2>/dev/null || true
chown -R wxcat:wxcat "$(dirname "$WXCAT_CONFIG")" "$WXCAT_DATA" 2>/dev/null || true

if [ ! -f "$WXCAT_CONFIG" ]; then
  cp /opt/wxcat/config.example.json "$WXCAT_CONFIG"
  chown wxcat:wxcat "$WXCAT_CONFIG" 2>/dev/null || true
  echo "[gateway] created default config at $WXCAT_CONFIG"
fi

echo "[gateway] starting wxcat (backend=${WXCAT_BACKEND:-wcf})"
exec su-exec wxcat /usr/local/bin/wxcat -c "$WXCAT_CONFIG"
