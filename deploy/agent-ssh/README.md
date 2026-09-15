# Добавить агенту (opencode) доступ к VDS

Даём opencode доступ к серверу так, чтобы он **физически не мог** выполнить произвольную команду или прочитать секреты.

## Как это устроено

- Отдельный непривилегированный юзер `agent` (не root).
- В его `authorized_keys` — **forced command**:
  `restrict,command="sudo -n /usr/local/sbin/ssau-agent-dispatch" <ключ>`.
  Какая бы команда ни пришла по SSH, запускается только диспетчер; `restrict` отключает pty, порты, agent/X11-forwarding.
- Диспетчер (`ssau-agent-dispatch`, root:root 0755) читает `SSH_ORIGINAL_COMMAND` и разрешает только whitelisted подкоманды:

  | Команда | Что делает |
  | --- | --- |
  | `status` | `systemctl status` по юнитам `ssau-*` |
  | `units` | `systemctl is-active` по юнитам `ssau-*` |
  | `logs <unit> [lines]` | хвост journal (unit из белого списка, lines ≤ 500) |
  | `restart <unit>` | рестарт юнита `ssau-*` |
  | `webapp-url` | текущий URL туннеля из `webapp_url` |
  | `env-keys` | **только имена** переменных из `.env`, без значений |
  | `recordings` | список файлов с размерами под `recordings/` |
  | `disk` | `df -h /`, `free -m` |

- `sudoers` разрешает юзеру только диспетчер: `agent ALL=(root) NOPASSWD: /usr/local/sbin/ssau-agent-dispatch`.
- На стороне opencode (`opencode.json`): чтение `.env` и `~/.ssh/**` запрещено, а из ssh разрешены ровно эти подкоманды.

Итог: `cat .env`, произвольный `bash`, чтение приватного ключа — недоступны ни на клиенте (permissions), ни на сервере (forced command).

## Один раз: ключ и доступ

1. Сгенерировать выделенный ключ (без пароля — чтобы агент ходил неинтерактивно):

   ```bash
   ssh-keygen -t ed25519 -f ~/.ssh/ssau_agent_ed25519 -C "opencode-agent" -N ""
   ```

2. Добавить хост в `~/.ssh/config`:

   ```
   Host ssau
     HostName 95.182.114.82
     User agent
     IdentityFile ~/.ssh/ssau_agent_ed25519
     IdentitiesOnly yes
     ServerAliveInterval 60
   ```

3. Установка доступа — **через CI/CD**, ничего на VDS не собирается:

   положить публичный ключ в GitHub-секрет репозитория:

   ```bash
   cat ~/.ssh/ssau_agent_ed25519.pub | gh secret set AGENT_SSH_PUBKEY --repo r4r1ty-tech/AIstudydevb2bsaas
   ```

   Дальше любой пуш в `main` (или ручной `workflow_dispatch`) вызывает `deploy/remote.sh`,
   который идемпотентно ставит юзера `agent`, диспетчер, sudoers и `authorized_keys` из
   этого секрета. Секрет не задан — шаг просто пропускается.

   Bootstrap без CI (если секрет ещё не выставлен) — один раз от root:

   ```bash
   ssh root@95.182.114.82 "bash -s -- '$(cat ~/.ssh/ssau_agent_ed25519.pub)'" \
     < deploy/agent-ssh/setup.sh
   ```

4. Проверить:

   ```bash
   ssh ssau status
   ssh ssau env-keys        # только имена, значений нет
   ssh -T ssau 'cat /opt/ssau-bot/.env'   # должно быть refused: unknown command
   ```

## Отзыв доступа

Удалить строку с ключом из `/home/agent/.ssh/authorized_keys` (или `userdel -r agent`). Приватный ключ на клиенте удалить: `rm ~/.ssh/ssau_agent_ed25519`.

## Заметки

- Ключ без пароля — осознанный компромисс: он всё равно ограничен forced command и непривилегированным юзером. Парольная фраза сломает неинтерактивные вызовы агента.
- `env-keys` не печатает значения и не может: диспетчер запускает только `grep` по именам.
- Деплой по-прежнему только через GitHub Actions; диспетчер не умеет деплоить.
