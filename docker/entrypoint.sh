#!/bin/bash
# Container entrypoint: prepare volumes, seed the baked Wine prefix (or install
# WeChat as a fallback), then hand control to supervisord which starts Xvfb,
# VNC, WeChat, the injector and WxCat.
set -euo pipefail

log() { printf '[entrypoint] %s\n' "$*"; }

export HOME=/home/wechat
export WINEPREFIX=/home/wechat/.wine
export WINEARCH=win64
export WINEDEBUG=-all
export WINEDLLOVERRIDES=mscoree,mshtml=

mkdir -p /app/config /app/data "/home/wechat/WeChat Files" /home/wechat/.vnc /home/wechat/.wine

if [ ! -f /app/config/wxcat.json ]; then
  log "creating default config at /app/config/wxcat.json"
  cp /opt/wxcat/config.example.json /app/config/wxcat.json
fi

# VNC password for noVNC. Default is intentionally simple; change it in .env.
VNC_PASSWORD="${VNC_PASSWORD:-wxcat}"
x11vnc -storepasswd "$VNC_PASSWORD" /home/wechat/.vnc/passwd >/dev/null 2>&1 || true

chown -R wechat:wechat /app /home/wechat/.vnc /home/wechat/.wine "/home/wechat/WeChat Files" 2>/dev/null || true

find_wechat() {
  find /home/wechat/.wine/drive_c -maxdepth 6 -iname 'WeChat.exe' -print -quit 2>/dev/null || true
}

seed_wine() {
  local seed=/opt/wine-seed
  if [ ! -d "$seed" ]; then
    return 1
  fi
  if [ -z "$(find "$seed/drive_c" -maxdepth 6 -iname 'WeChat.exe' -print -quit 2>/dev/null)" ]; then
    return 1
  fi
  log "seeding Wine prefix from the image (WeChat is already bundled)"
  cp -a "$seed/." /home/wechat/.wine/
  chown -R wechat:wechat /home/wechat/.wine 2>/dev/null || true
  return 0
}

install_wechat() {
  log "installing WeChat (fallback, this takes a few minutes)"
  su -s /bin/bash wechat -c 'export HOME=/home/wechat WINEPREFIX=/home/wechat/.wine WINEARCH=win64 WINEDEBUG=-all; xvfb-run -a wineboot -u' || true
  su -s /bin/bash wechat -c 'export HOME=/home/wechat WINEPREFIX=/home/wechat/.wine WINEARCH=win64 WINEDEBUG=-all; xvfb-run -a wine /opt/wcf/WeChatSetup.exe /S' || true
  # Give the installer time to finish writing.
  for _ in $(seq 1 60); do
    if [ -n "$(find_wechat)" ]; then break; fi
    sleep 2
  done
}

if [ -z "$(find_wechat)" ]; then
  seed_wine || install_wechat
fi

EXE="$(find_wechat)"
if [ -z "$EXE" ]; then
  log "WeChat.exe not found after install; the injector will keep retrying"
else
  log "WeChat found at $EXE"
  install -m 0644 /opt/wcf/sdk.dll /opt/wcf/spy.dll "$(dirname "$EXE")/" || true
fi

log "starting supervisord"
exec /usr/bin/supervisord -c /etc/supervisor/supervisord.conf
