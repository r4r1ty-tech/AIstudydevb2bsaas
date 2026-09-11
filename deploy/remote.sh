#!/bin/bash
set -euo pipefail

APP=/opt/ssau-bot
SRC=${1:-/tmp/ssau-deploy}

find_one() {
  local name=$1
  local cand
  for cand in "$SRC/$name" "$SRC/bin/$name" "$SRC/deploy/$name"; do
    if [[ -f "$cand" ]]; then
      echo "$cand"
      return 0
    fi
  done
  return 1
}

need() {
  local p
  p=$(find_one "$1") || {
    echo "не нашёл $1 в $SRC:" >&2
    find "$SRC" -type f >&2 || true
    exit 1
  }
  echo "$p"
}

tg=$(need tg)
rasp=$(need rasp)
panel=$(need panel)
bbb=$(need bbb)
unit_tg=$(need ssau-tg.service)
unit_rasp=$(need ssau-rasp.service)
unit_panel=$(need ssau-panel.service)
unit_bbb=$(need ssau-bbb.service)
target=$(need ssau.target)

install -d -m 755 "$APP" "$APP/data" "$APP/recordings"

if ! command -v chromium >/dev/null 2>&1 || ! command -v ffmpeg >/dev/null 2>&1; then
  export DEBIAN_FRONTEND=noninteractive
  apt-get update -qq
  apt-get install -y -qq chromium ffmpeg fonts-liberation ca-certificates
fi

install -m 755 "$tg" "$APP/tg"
install -m 755 "$rasp" "$APP/rasp"
install -m 755 "$panel" "$APP/panel"
install -m 755 "$bbb" "$APP/bbb"
install -m 644 "$unit_tg" /etc/systemd/system/ssau-tg.service
install -m 644 "$unit_rasp" /etc/systemd/system/ssau-rasp.service
install -m 644 "$unit_panel" /etc/systemd/system/ssau-panel.service
install -m 644 "$unit_bbb" /etc/systemd/system/ssau-bbb.service
install -m 644 "$target" /etc/systemd/system/ssau.target

systemctl disable --now ssau-bot.service 2>/dev/null || true
rm -f /etc/systemd/system/ssau-bot.service

systemctl daemon-reload
systemctl enable ssau.target ssau-tg.service ssau-rasp.service ssau-panel.service ssau-bbb.service

if [[ ! -f "$APP/.env" ]]; then
  echo "бинарники в $APP, но нет $APP/.env — сервисы не стартую"
  echo "скопируй .env на сервер один раз и перезапусти workflow"
  exit 0
fi

systemctl restart ssau-tg.service ssau-rasp.service ssau-panel.service ssau-bbb.service
sleep 1
systemctl --no-pager --full status ssau-tg.service ssau-rasp.service ssau-panel.service ssau-bbb.service
