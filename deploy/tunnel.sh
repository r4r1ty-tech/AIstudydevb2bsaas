#!/bin/bash
set -euo pipefail

ENV_FILE=/opt/ssau-bot/.env
URL_FILE=/opt/ssau-bot/webapp_url
BIN=/usr/local/bin/cloudflared
ORIGIN=http://127.0.0.1:8080

set_webapp_url() {
  local url=$1
  printf '%s\0' "$url" | ENV_FILE="$ENV_FILE" python3 -c '
import os, sys, tempfile
from pathlib import Path

path = Path(os.environ["ENV_FILE"])
url = sys.stdin.read().split("\0", 1)[0].rstrip("/")
text = path.read_text(encoding="utf-8") if path.exists() else ""
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
    path.parent.mkdir(parents=True, exist_ok=True)
    fd, tmp_name = tempfile.mkstemp(prefix=path.name + ".", suffix=".tmp", dir=str(path.parent))
    try:
        os.chmod(tmp_name, 0o600)
        with os.fdopen(fd, "w", encoding="utf-8") as f:
            f.write("\n".join(lines) + "\n")
        os.replace(tmp_name, path)
    except Exception:
        if os.path.exists(tmp_name):
            os.unlink(tmp_name)
        raise
print("changed" if changed else "same")
'
}

write_url_file() {
  local url=$1
  printf '%s\n' "$url" > "$URL_FILE.tmp"
  mv "$URL_FILE.tmp" "$URL_FILE"
}

if [[ ! -x "$BIN" ]]; then
  echo "нет $BIN" >&2
  exit 1
fi

echo "tunnel origin $ORIGIN"
"$BIN" tunnel --no-autoupdate --url "$ORIGIN" 2>&1 | while IFS= read -r line; do
  printf '%s\n' "$line"
  if [[ "$line" =~ https://([a-zA-Z0-9-]+)\.trycloudflare\.com ]]; then
    sub="${BASH_REMATCH[1]}"
    if [[ "${sub,,}" != "api" ]]; then
      url="${BASH_REMATCH[0]}"
      write_url_file "$url"
      st=$(set_webapp_url "$url")
      echo "webapp $url file=saved env=$st"
    fi
  fi
done
