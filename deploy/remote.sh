#!/bin/bash
set -euo pipefail

APP=/opt/ssau-bot
SRC=${1:-/tmp/ssau-deploy}

bin=
unit=
remote_helper=
for cand in "$SRC/bot" "$SRC/bin/bot"; do
  if [[ -f "$cand" ]]; then
    bin=$cand
    break
  fi
done
for cand in "$SRC/ssau-bot.service" "$SRC/deploy/ssau-bot.service"; do
  if [[ -f "$cand" ]]; then
    unit=$cand
    break
  fi
done

if [[ -z "$bin" || -z "$unit" ]]; then
  echo "не нашёл bot или unit в $SRC:" >&2
  find "$SRC" -type f >&2 || true
  exit 1
fi

install -d -m 755 "$APP" "$APP/data" "$APP/recordings"
install -m 755 "$bin" "$APP/bot"
install -m 644 "$unit" /etc/systemd/system/ssau-bot.service

systemctl daemon-reload
systemctl enable ssau-bot.service

if [[ ! -f "$APP/.env" ]]; then
  echo "бинарник в $APP, но нет $APP/.env — сервис не стартую"
  echo "скопируй .env на сервер один раз и перезапусти workflow"
  exit 0
fi

systemctl restart ssau-bot.service
sleep 1
systemctl --no-pager --full status ssau-bot.service
