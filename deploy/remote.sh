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

set_env_vars() {
  python3 - "$APP/.env" "$@" <<'PY'
import sys
from pathlib import Path

path = Path(sys.argv[1])
pairs = []
for arg in sys.argv[2:]:
    key, _, val = arg.partition("=")
    if key and val:
        pairs.append((key, val))
if not pairs:
    sys.exit(0)
lines = path.read_text().splitlines() if path.exists() else []
for key, val in pairs:
    found = False
    for i, line in enumerate(lines):
        if line.startswith(key + "="):
            lines[i] = key + "=" + val
            found = True
            break
    if not found:
        lines.append(key + "=" + val)
path.write_text("\n".join(lines) + "\n")
path.chmod(0o600)
PY
}

tg=$(need tg)
rasp=$(need rasp)
panel=$(need panel)
bbb=$(need bbb)
unit_tg=$(need ssau-tg.service)
unit_rasp=$(need ssau-rasp.service)
unit_panel=$(need ssau-panel.service)
unit_bbb=$(need ssau-bbb.service)
unit_tunnel=$(need ssau-tunnel.service)
tunnel_sh=$(need tunnel.sh)
audio_sh=$(need audio.sh)
wake_py=$(need wake.py)
target=$(need ssau.target)

install -d -m 755 "$APP" "$APP/data" "$APP/recordings" "$APP/chrome" "$APP/chrome-rec"

need_pkgs=()
for p in chromium ffmpeg fonts-liberation ca-certificates pulseaudio pulseaudio-utils python3 python3-pip unzip; do
  if ! dpkg -s "$p" >/dev/null 2>&1; then
    need_pkgs+=("$p")
  fi
done
if ((${#need_pkgs[@]})); then
  export DEBIAN_FRONTEND=noninteractive
  apt-get update -qq
  apt-get install -y -qq "${need_pkgs[@]}"
fi

ensure_swap() {
  local file=/swapfile
  local want_g=8
  local avail_k reserve_k max_g
  if swapon --noheadings --show 2>/dev/null | grep -q .; then
    echo "swap already on"
    return 0
  fi
  avail_k=$(df -Pk / | awk 'NR==2{print $4}')
  reserve_k=$((10 * 1024 * 1024)) # keep ~10G
  max_g=$(( (avail_k - reserve_k) / 1024 / 1024 ))
  if (( max_g < 2 )); then
    echo "disk too tight for swap, skip"
    return 0
  fi
  if (( want_g > max_g )); then
    want_g=$max_g
  fi
  if [[ -f "$file" ]]; then
    chmod 600 "$file"
    swapon "$file" || true
    if swapon --noheadings --show 2>/dev/null | grep -q .; then
      grep -qE '^/swapfile ' /etc/fstab || echo '/swapfile none swap sw 0 0' >> /etc/fstab
      return 0
    fi
    swapoff "$file" 2>/dev/null || true
    rm -f "$file"
  fi
  fallocate -l "${want_g}G" "$file"
  chmod 600 "$file"
  mkswap "$file"
  swapon "$file"
  grep -qE '^/swapfile ' /etc/fstab || echo '/swapfile none swap sw 0 0' >> /etc/fstab
  echo "swap ${want_g}G on"
}
ensure_swap

ensure_vosk() {
  local model="$APP/vosk-model"
  if ! python3 -c "import vosk" >/dev/null 2>&1; then
    python3 -m pip install --break-system-packages -q vosk || {
      echo "vosk pip failed — пейджер будет молчать"
      return 0
    }
  fi
  if [[ -f "$model/am/final.mdl" || -f "$model/conf/model.conf" ]]; then
    echo "vosk model already there"
    return 0
  fi
  local zip=/tmp/vosk-model-small-ru-0.22.zip
  if ! curl -fsSL -o "$zip" "https://alphacephei.com/vosk/models/vosk-model-small-ru-0.22.zip"; then
    echo "vosk model download failed — пейджер будет молчать"
    return 0
  fi
  rm -rf "$model" "$APP/vosk-model-small-ru-0.22"
  unzip -q "$zip" -d "$APP"
  rm -f "$zip"
  if [[ -d "$APP/vosk-model-small-ru-0.22" ]]; then
    mv "$APP/vosk-model-small-ru-0.22" "$model"
  fi
  echo "vosk model installed"
}
ensure_vosk

if [[ ! -x /usr/local/bin/cloudflared ]]; then
  curl -fsSL -o /usr/local/bin/cloudflared "https://github.com/cloudflare/cloudflared/releases/latest/download/cloudflared-linux-amd64"
  chmod 755 /usr/local/bin/cloudflared
fi

install -m 755 "$tg" "$APP/tg"
install -m 755 "$rasp" "$APP/rasp"
install -m 755 "$panel" "$APP/panel"
install -m 755 "$bbb" "$APP/bbb"
install -m 644 "$unit_tg" /etc/systemd/system/ssau-tg.service
install -m 644 "$unit_rasp" /etc/systemd/system/ssau-rasp.service
install -m 644 "$unit_panel" /etc/systemd/system/ssau-panel.service
install -m 644 "$unit_bbb" /etc/systemd/system/ssau-bbb.service
install -m 644 "$unit_tunnel" /etc/systemd/system/ssau-tunnel.service
install -m 755 "$tunnel_sh" "$APP/tunnel.sh"
install -m 755 "$audio_sh" "$APP/audio.sh"
install -m 755 "$wake_py" "$APP/wake.py"
install -m 644 "$target" /etc/systemd/system/ssau.target

systemctl disable --now ssau-bot.service 2>/dev/null || true
rm -f /etc/systemd/system/ssau-bot.service

systemctl daemon-reload
systemctl enable ssau.target ssau-tg.service ssau-rasp.service ssau-panel.service ssau-bbb.service ssau-tunnel.service

if [[ ! -f "$APP/.env" ]]; then
  echo "бинарники в $APP, но нет $APP/.env — сервисы не стартую"
  echo "скопируй .env на сервер один раз и перезапусти workflow"
  exit 0
fi

grep -q '^LECTURE_PAUSE=' "$APP/.env" || echo 'LECTURE_PAUSE=1' >> "$APP/.env"
grep -q '^CHROME_USER_DATA_DIR=' "$APP/.env" || echo 'CHROME_USER_DATA_DIR=/opt/ssau-bot/chrome' >> "$APP/.env"
grep -q '^VOSK_MODEL=' "$APP/.env" || echo 'VOSK_MODEL=/opt/ssau-bot/vosk-model' >> "$APP/.env"
grep -q '^VOSK_SCRIPT=' "$APP/.env" || echo 'VOSK_SCRIPT=/opt/ssau-bot/wake.py' >> "$APP/.env"
grep -q '^LOG_LEVEL=' "$APP/.env" || echo 'LOG_LEVEL=info' >> "$APP/.env"
grep -q '^LOG_FILE=' "$APP/.env" || echo 'LOG_FILE=/opt/ssau-bot/ssau.log' >> "$APP/.env"
grep -q '^WEBAPP_URL_FILE=' "$APP/.env" || echo 'WEBAPP_URL_FILE=/opt/ssau-bot/webapp_url' >> "$APP/.env"

set_env_vars \
  "DEEPSEEK_API_KEY=${DEEPSEEK_API_KEY:-}" \
  "DEEPSEEK_API_URL=${DEEPSEEK_API_URL:-}" \
  "DEEPSEEK_MODEL=${DEEPSEEK_MODEL:-}"

if [[ -n "${AGENT_SSH_PUBKEY:-}" ]]; then
  if [[ -f "$SRC/agent-ssh/setup.sh" ]]; then
    bash "$SRC/agent-ssh/setup.sh" "$AGENT_SSH_PUBKEY" || echo "agent-ssh: setup не прошёл, продолжаю"
  else
    echo "agent-ssh: нет $SRC/agent-ssh/setup.sh"
  fi
fi

bbb_was_active=0
if systemctl is-active --quiet ssau-bbb.service; then
  bbb_was_active=1
fi

systemctl restart ssau-tg.service ssau-rasp.service ssau-panel.service ssau-tunnel.service
if [[ "$bbb_was_active" -eq 1 ]]; then
  systemctl restart ssau-bbb.service
fi
sleep 1
systemctl --no-pager --full status ssau-tg.service ssau-rasp.service ssau-panel.service ssau-tunnel.service
if [[ "$bbb_was_active" -eq 1 ]]; then
  systemctl --no-pager --full status ssau-bbb.service
else
  echo "ssau-bbb был выключен — бинарник обновил, сервис не стартовал"
fi
