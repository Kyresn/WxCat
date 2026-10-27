#!/bin/bash
# Locate WeChat.exe inside the Wine prefix and run it.
set -u

export HOME=/home/wechat
export WINEPREFIX=/home/wechat/.wine
export WINEARCH=win64
export WINEDEBUG=-all
export WINEDLLOVERRIDES=mscoree,mshtml=
export DISPLAY=:1

EXE="$(find /home/wechat/.wine/drive_c -maxdepth 6 -iname 'WeChat.exe' -print -quit 2>/dev/null || true)"
if [ -z "$EXE" ]; then
  echo "[wechat] WeChat.exe not found in the prefix; was the install step successful?"
  exit 1
fi

WIN_PATH="$(winepath -w "$EXE" 2>/dev/null || true)"
if [ -z "$WIN_PATH" ]; then
  echo "[wechat] winepath failed for $EXE"
  exit 1
fi

echo "[wechat] starting $WIN_PATH"
exec wine "$WIN_PATH"
