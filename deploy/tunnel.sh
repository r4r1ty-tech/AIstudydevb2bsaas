#!/bin/bash
set -euo pipefail

ENV_FILE=/opt/ssau-bot/.env
BIN=/usr/local/bin/cloudflared
ORIGIN=http://127.0.0.1:8080

set_webapp_url() {
  local url=$1
  python3 - "$ENV_FILE" "$url" <<'PY'
import sys
from pathlib import Path
path = Path(sys.argv[1])
url = sys.argv[2].rstrip("/")
text = path.read_text() if path.exists() else ""
lines = []
found = False
changed = False
for line in text.splitlines():
    if line.startswith("WEBAPP_PUBLIC_URL="):
        old = line.split("=", 1)[1].strip()
        lines.append("WEBAPP_PUBLIC_URL=" + url)
        found = True
        changed = old.rstrip("/") != url
    else:
        lines.append(line)
if not found:
    lines.append("WEBAPP_PUBLIC_URL=" + url)
    changed = True
if changed:
    path.write_text("\n".join(lines) + "\n")
    path.chmod(0o600)
print("changed" if changed else "same")
PY
}

if [[ ! -x "$BIN" ]]; then
  echo "нет $BIN" >&2
  exit 1
fi

echo "tunnel origin $ORIGIN"
"$BIN" tunnel --no-autoupdate --url "$ORIGIN" 2>&1 | while IFS= read -r line; do
  printf '%s\n' "$line"
  if [[ "$line" =~ https://[a-zA-Z0-9.-]+\.trycloudflare\.com ]]; then
    url="${BASH_REMATCH[0]}"
    st=$(set_webapp_url "$url")
    echo "webapp $url $st"
    if [[ "$st" == "changed" ]]; then
      systemctl restart ssau-tg.service || true
    fi
  fi
done
