# SSAU lecture bot

Четыре бинарника на Go, один VDS, общая sqlite. Не монолит.

| Бинарник | Зачем |
| --- | --- |
| `tg` | Telegram: вайтлист, онбординг, T-15 |
| `rasp` | парсер расписания |
| `panel` | Mini App + API |
| `bbb` | гостевой заход в BBB |

Полный замысел — [PLAN.md](PLAN.md).

```bash
go build -o bin/tg ./cmd/tg
go build -o bin/rasp ./cmd/rasp
go build -o bin/panel ./cmd/panel
go build -o bin/bbb ./cmd/bbb
```

Секреты в `.env` на сервере, в git не класть. Один файл на все процессы.

## Push → VDS

Пуш в `main` гоняет тесты в GitHub Actions, собирает linux-бинарники и выкатывает `/opt/ssau-bot` + `ssau.target`.

Один раз:

1. Секрет `VDS_SSH_KEY` — ключ, которым `root@95.182.114.82` пускает.
2. На сервере этот ключ в `authorized_keys`.
3. `.env` руками: `/opt/ssau-bot/.env`.

Дальше любой пуш в `main` сам деплоит. Ручной прогон: Actions → Test and deploy VDS → Run workflow.
