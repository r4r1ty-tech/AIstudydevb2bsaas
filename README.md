# SSAU lecture bot

Один бинарник на Go: Telegram-бот + воркер на VDS. Ходит гостем в BBB Самарского университета по расписанию группы, пишет звук лекции и шлёт алерты по вейквордам.

Полный замысел и решения — в [PLAN.md](PLAN.md).

```bash
go build -o bin/bot ./cmd/bot
```

Секреты (токен бота, SOCKS5) только в `.env` на сервере, в git не класть.

## Push → VDS

Пуш в ветку `plan` собирает linux-бинарник в GitHub Actions и выкатывает на VDS: `/opt/ssau-bot`, unit `ssau-bot.service`.

Один раз:

1. В репе секрет `VDS_SSH_KEY` — приватный ключ, которым `root@95.182.114.82` пускает.
2. На сервере публичная часть этого ключа в `/root/.ssh/authorized_keys`.
3. `.env` на сервер руками: `/opt/ssau-bot/.env` (workflow его не трогает).

Дальше любой пуш в `plan` сам деплоит. Ручной прогон: Actions → Deploy VDS → Run workflow.
