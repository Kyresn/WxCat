#!/bin/bash
# Inject sdk.dll into WeChat.exe, then keep watching: if WeChat restarts or the
# WeChatFerry RPC port disappears, inject again.
set -u

export HOME=/home/wechat
export WINEPREFIX=/home/wechat/.wine
export WINEARCH=win64
export WINEDEBUG=-all
export WINEDLLOVERRIDES=mscoree,mshtml=
export DISPLAY=:1

log() { printf '[inject] %s\n' "$*"; }

find_exe() {
  find /home/wechat/.wine/drive_c -maxdepth 6 -iname 'WeChat.exe' -print -quit 2>/dev/null || true
}

rpc_up() {
  nc -z 127.0.0.1 10086 >/dev/null 2>&1
}

inject_once() {
  local exe dir win_dir dll
  exe="$(find_exe)"
  if [ -z "$exe" ]; then
    log "WeChat.exe not found"
    return 1
  fi
  dir="$(dirname "$exe")"
  cp -f /opt/wcf/sdk.dll /opt/wcf/spy.dll "$dir/" 2>/dev/null || true

  win_dir="$(winepath -w "$dir" 2>/dev/null || true)"
  if [ -z "$win_dir" ]; then
    log "winepath failed"
    return 1
  fi
  dll="${win_dir}\\sdk.dll"

  for attempt in $(seq 1 10); do
    log "injection attempt $attempt: $dll"
    # Success is measured by the RPC port actually opening, not by the
    # injector's exit code: LoadLibraryA can fail silently inside WeChat.
    if wine /opt/wcf/wxinject.exe "$dll"; then
      for _ in $(seq 1 10); do
        if rpc_up; then
          log "injection succeeded (rpc port is up)"
          return 0
        fi
        sleep 1
      done
      log "injector exited 0 but the rpc port did not open; retrying"
      continue
    fi
    sleep 5
  done
  log "injection failed after 10 attempts"
  return 1
}

# Wait for WeChat to start for the first time.
for _ in $(seq 1 150); do
  if [ -n "$(find_exe)" ] && pgrep -if 'wechat.exe' >/dev/null 2>&1; then
    break
  fi
  sleep 2
done

# The login window needs a moment before the hook can attach cleanly.
sleep 4

last_pid=""
while true; do
  pid="$(pgrep -if 'wechat.exe' | head -n1 || true)"
  if [ -z "$pid" ]; then
    last_pid=""
    sleep 5
    continue
  fi

  if [ "$pid" != "$last_pid" ] || ! rpc_up; then
    if inject_once; then
      last_pid="$pid"
    fi
  fi
  sleep 5
done
