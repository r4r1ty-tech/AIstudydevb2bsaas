#!/bin/bash
set -euo pipefail

# One-time setup on the VDS. Gives the coding agent a key that can run ONLY
# the whitelisted dispatcher below, as a non-login, non-root user.
#
# Run on the VDS as root:
#   ssh root@HOST "bash -s -- 'ssh-ed25519 AAAA... opencode-agent'" < setup.sh
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
UNIT_LIST="ssau-tg ssau-rasp ssau-panel ssau-bbb ssau-tunnel ssau.target"

usage() {
  cat <<'EOF'
opencode agent dispatcher. allowed:
  status                    systemctl status for ssau units
  units                     systemctl is-active for ssau units
  logs <unit> [lines]       journalctl -u <unit> (lines <= 500, default 100)
  restart <unit>            systemctl restart <ssau unit>
  webapp-url                current tunnel url (from file)
  env-keys                  NAMES of keys in .env only, never values
  disk                      df -h / and free -m
EOF
}

is_unit() {
  case " ${UNIT_LIST} " in
    *" $1 "*) return 0 ;;
    *) return 1 ;;
  esac
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
    if [[ "${arg}" == *" "* ]]; then
      lines="${arg#* }"
    fi
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
    if [[ ! -f "${APP}/.env" ]]; then echo "no ${APP}/.env" >&2; exit 1; fi
    exec grep -oE '^[A-Za-z_][A-Za-z0-9_]*=' "${APP}/.env" | tr -d '=' | sort
    ;;
  disk )
    exec bash -c 'df -h /; echo; free -m'
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