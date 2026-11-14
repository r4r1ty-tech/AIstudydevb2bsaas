#!/bin/bash
# Полный сброс данных бота на VDS: БД и конспекты.
# .env, бинарники и vosk не трогаем.
set -euo pipefail

APP=${APP:-/opt/ssau-bot}

echo "stop services"
systemctl stop ssau-tg.service ssau-bbb.service ssau-panel.service ssau-rasp.service 2>/dev/null || true

echo "wipe sqlite"
rm -f "$APP/data/"*.db "$APP/data/"*.db-wal "$APP/data/"*.db-shm "$APP/data/"*.db-journal 2>/dev/null || true

echo "wipe recordings / lecture packs"
if [[ -d "$APP/recordings" ]]; then
  find "$APP/recordings" -mindepth 1 -maxdepth 1 -exec rm -rf {} +
fi

mkdir -p "$APP/data" "$APP/recordings"

echo "start services"
systemctl start ssau-rasp.service ssau-tg.service ssau-panel.service ssau-bbb.service
systemctl is-active ssau-tg ssau-bbb ssau-panel ssau-rasp || true

echo "reset done: users/fio/links/lessons/intents/packs/events cleared; заново: «Первый вход» в панели (имя для BBB и подгруппа), пароль — кнопкой в боте"
