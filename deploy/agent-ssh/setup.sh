#!/bin/bash
set -euo pipefail

# One-time setup on the VDS. Gives the coding agent a key that can run ONLY
# the whitelisted dispatcher below, as a non-login, non-root user.
#
# Run on the VDS as root:
#   ssh root@HOST "bash -s -- 'ssh-ed25519 AAAA... opencode-agent'" < setup.sh
#
# The dispatcher body below must stay identical to deploy/agent-ssh/dispatch.sh.
#
# Idempotent. Never prints or stores private material.

USER_NAME=agent
APP=/opt/ssau-bot
DISPATCH=/usr/local/sbin/ssau-agent-dispatch
SUDOERS=/etc/sudoers.d/ssau-agent
PUBKEY="${1:-}"

if [[ "${EUID}" -ne 0 ]]; then
  echo "run as root" >&2
  exit 1
fi
if [[ -z "${PUBKEY}" ]]; then
  echo "usage: bash setup.sh '<ssh public key>'" >&2
  exit 2
fi
case "${PUBKEY}" in
  ssh-ed25519\ *|ssh-rsa\ *|ecdsa-sha2-*\ *) ;;
  *) echo "not an ssh public key" >&2; exit 2 ;;
esac

id -u "${USER_NAME}" >/dev/null 2>&1 || useradd -m -s /bin/bash "${USER_NAME}"

install -m 0755 /dev/stdin "${DISPATCH}" <<'DISPATCH_EOF'
#!/bin/bash
set -euo pipefail

APP=/opt/ssau-bot
URL_FILE="${APP}/webapp_url"
ENV_FILE="${APP}/.env"
UNIT_LIST="ssau-tg ssau-rasp ssau-panel ssau-bbb ssau-tunnel ssau.target"
BASE_URL="http://127.0.0.1:8080"

usage() {
  cat <<'EOF'
opencode agent dispatcher. allowed:
  status                    systemctl status for ssau units
  units                     systemctl is-active for ssau units
  logs <unit> [lines]       journalctl -u <unit> (lines <= 500, default 100)
  restart <unit>            systemctl restart <ssau unit>
  webapp-url                current tunnel url (from file)
  env-keys                  key names + set/empty only, never values
  recordings                file listing with sizes under recordings/
  disk                      df -h / and free -m

  test                      read current test join state
  test <url>                arm test bbb url
  test-name [name]          test guest name (no arg -> "тест")
  test-dummy                join test room passive
  test-listen               join test room listening (records)
  test-leave                leave test room
  test-log-level <level>    debug|info|warn|error then restart tg,bbb,panel
EOF
}

is_unit() {
  case " ${UNIT_LIST} " in
    *" $1 "*) return 0 ;;
    *) return 1 ;;
  esac
}

json_field() {
  python3 -c 'import json,sys; sys.stdout.write(json.dumps({sys.argv[1]: sys.argv[2]}))' "$1" "$2"
}

set_env_var() {
  python3 - "${ENV_FILE}" "$1" "$2" <<'PY'
import sys
from pathlib import Path
path = Path(sys.argv[1]); key = sys.argv[2]; val = sys.argv[3]
lines = path.read_text().splitlines() if path.exists() else []
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

test_password() {
  awk -F= '/^PANEL_PASSWORD=/{print substr($0, index($0,"=")+1)}' "${ENV_FILE}"
}

test_get() {
  local pass
  pass=$(test_password)
  if [[ -z "${pass}" ]]; then echo "PANEL_PASSWORD empty in ${ENV_FILE}" >&2; exit 1; fi
  exec curl -sS -w '\nHTTP %{http_code}\n' "${BASE_URL}/api/test" -H "X-Panel-Password: ${pass}"
}

test_post() {
  local pass
  pass=$(test_password)
  if [[ -z "${pass}" ]]; then echo "PANEL_PASSWORD empty in ${ENV_FILE}" >&2; exit 1; fi
  exec curl -sS -w '\nHTTP %{http_code}\n' -X POST "${BASE_URL}/api/test" \
    -H "X-Panel-Password: ${pass}" -H 'Content-Type: application/json' \
    --data-binary "$1"
}

cmd="${SSH_ORIGINAL_COMMAND:-}"
cmd="$(printf '%s' "${cmd}" | tr -s '[:space:]' ' ' | sed -e 's/^ //' -e 's/ $//')"
verb="${cmd%% *}"
arg=""
if [[ "${cmd}" == *" "* ]]; then
  arg="${cmd#* }"
fi

case "${verb}" in
  "" ) usage >&2; exit 2 ;;
  status ) exec systemctl --no-pager --full status ${UNIT_LIST} ;;
  units ) exec systemctl is-active ${UNIT_LIST} ;;
  logs )
    unit="${arg%% *}"
    lines=""
    if [[ "${arg}" == *" "* ]]; then lines="${arg#* }"; fi
    if [[ -z "${unit}" ]]; then echo "logs <unit> [lines]" >&2; exit 2; fi
    if ! is_unit "${unit}"; then echo "unknown unit: ${unit}" >&2; exit 2; fi
    if [[ ! "${lines}" =~ ^[0-9]+$ ]]; then lines=100; fi
    if (( lines > 500 )); then lines=500; fi
    exec journalctl -u "${unit}" -n "${lines}" --no-pager
    ;;
  restart )
    if ! is_unit "${arg}"; then echo "unknown unit: ${arg}" >&2; exit 2; fi
    exec systemctl restart "${arg}"
    ;;
  webapp-url )
    if [[ ! -f "${URL_FILE}" ]]; then echo "no ${URL_FILE}" >&2; exit 1; fi
    exec cat "${URL_FILE}"
    ;;
  env-keys )
    if [[ ! -f "${ENV_FILE}" ]]; then echo "no ${ENV_FILE}" >&2; exit 1; fi
    awk -F= '/^[A-Za-z_][A-Za-z0-9_]*=/ {v=substr($0,index($0,"=")+1); gsub(/\r/,"",v); print $1 (length(v)?" set":" empty")}' "${ENV_FILE}" | sort
    exit 0
    ;;
  recordings )
    if [[ ! -d "${APP}/recordings" ]]; then echo "no ${APP}/recordings" >&2; exit 1; fi
    exec find "${APP}/recordings" -maxdepth 3 -type f -printf '%10s  %P\n'
    ;;
  disk )
    exec bash -c 'df -h /; echo; free -m'
    ;;
  test )
    if [[ -z "${arg}" ]]; then test_get; fi
    if [[ "${#arg}" -gt 512 || "${arg}" != https://* ]]; then echo "test <url>: https URL expected" >&2; exit 2; fi
    test_post "$(json_field url "${arg}")"
    ;;
  test-name )
    name="${arg:-тест}"
    if [[ "${#name}" -gt 64 ]]; then echo "test-name: too long" >&2; exit 2; fi
    test_post "$(json_field name "${name}")"
    ;;
  test-dummy )
    test_post "$(json_field want dummy)"
    ;;
  test-listen )
    test_post "$(json_field want listen)"
    ;;
  test-leave )
    test_post "$(json_field want off)"
    ;;
  test-log-level )
    case "${arg}" in
      debug|info|warn|error) ;;
      *) echo "test-log-level <debug|info|warn|error>" >&2; exit 2 ;;
    esac
    set_env_var LOG_LEVEL "${arg}"
    exec systemctl restart ssau-tg.service ssau-bbb.service ssau-panel.service
    ;;
  * )
    echo "unknown command: ${verb}" >&2
    usage >&2
    exit 2
    ;;
esac
DISPATCH_EOF

{
  printf 'Defaults:%s env_keep += "SSH_ORIGINAL_COMMAND"\n' "${USER_NAME}"
  printf '%s ALL=(root) NOPASSWD: %s\n' "${USER_NAME}" "${DISPATCH}"
} > "${SUDOERS}"
chmod 0440 "${SUDOERS}"
visudo -cf "${SUDOERS}"

install -d -m 0700 -o "${USER_NAME}" -g "${USER_NAME}" "/home/${USER_NAME}/.ssh"
AUTH="/home/${USER_NAME}/.ssh/authorized_keys"
printf 'restrict,command="sudo -n %s" %s\n' "${DISPATCH}" "${PUBKEY}" > "${AUTH}"
chown "${USER_NAME}:${USER_NAME}" "${AUTH}"
chmod 0600 "${AUTH}"

echo "OK: user '${USER_NAME}' ready."
echo "test from the client: ssh ssau status"