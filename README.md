# SSAU lecture bot

Четыре бинарника на Go, один VDS, общая sqlite. Не монолит.

| Бинарник | Unit | Зачем |
| --- | --- | --- |
| `tg` | `ssau-tg.service` | Telegram: вайтлист, онбординг, T-15, память ссылок BBB, кнопка Mini App |
| `rasp` | `ssau-rasp.service` | Парсер расписания `ssau.ru/rasp` + cron 7/15/19/22 |
| `panel` | `ssau-panel.service` | HTTPS Mini App + JSON API. Единственный, кто слушает порт |
| `bbb` | `ssau-bbb.service` | Гостевой join в BBB через Chromium, listen-only, запись, вейкворды |

Полный замысел, решения и этапы — [PLAN.md](PLAN.md). Устройство и потоки данных — [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md). Эксплуатация, конфиг, логи и деплой — [docs/OPERATIONS.md](docs/OPERATIONS.md).

## Как это живёт

```
Telegram ──► tg ──┐
                  │
rasp cron ────────┼──► sqlite (WAL)
                  │
panel (Mini App) ─┤
                  │
bbb (Chromium) ───┘
```

Связка — sqlite, не gRPC. `ssau.target` поднимает все четыре. Папки `internal/*` — общая библиотека, в проде сами по себе не бегают.

## Быстрый старт

```bash
cp env.example .env      # вписать TELEGRAM_BOT_TOKEN и PANEL_PASSWORD
go build -o bin/tg ./cmd/tg
go build -o bin/rasp ./cmd/rasp
go build -o bin/panel ./cmd/panel
go build -o bin/bbb ./cmd/bbb
```

Для локальной разработки достаточно токена бота: парсер `rasp` ходит на публичную страницу без ключей, а `BBB_DRY_RUN=1` подменяет Chromium заглушкой.

## Тесты

```bash
go test ./...                 # локально
CGO_ENABLED=0 go test ./...   # то же, что гоняет CI
go test ./... -cover          # покрытие по пакетам
go vet ./...
```

Юнит-тесты лежат рядом с кодом (`*_test.go`). Тесты не требуют сети: внешние API подменяются `httptest`, Chromium — заглушками (`DryJoiner`).

## CI/CD

`.github/workflows/deploy.yml`:

- **pull_request → main**: только `CGO_ENABLED=0 go test ./...`.
- **push → main**: тесты → сборка `linux/amd64` четырёх бинарников → `scp` в `/opt/ssau-bot` → перезапуск systemd через `deploy/remote.sh`.
- **workflow_dispatch**: ручной прогон теста и деплоя.

Секреты репозитория: `VDS_SSH_KEY` (обязателен), `VDS_HOST`, `VDS_USER`, `VDS_PORT` (по умолчанию `95.182.114.82`, `root`, `22`). `.env` на сервере в деплой не входит — правится руками один раз.

## Логи

Все процессы пишут через `internal/logx` с уровнями `debug/info/warn/error` в stderr и, если задан `LOG_FILE`, в общий файл. Подробности и рецепты диагностики — [docs/OPERATIONS.md](docs/OPERATIONS.md#логи).

```bash
journalctl -u ssau-bbb -f
LOG_LEVEL=debug   # в /opt/ssau-bot/.env для подробного разбора
```

## Риски

Посещаемость гостем и запись пары — серая зона правил универа и BBB. Репозиторий приватный. Не светить токены и записи.