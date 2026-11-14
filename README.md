# SSAU lecture bot

Четыре бинарника на Go, один VDS, общая sqlite. Не монолит.

| Бинарник | Unit | Зачем |
| --- | --- | --- |
| `tg` | `ssau-tg.service` | Telegram: только личный пароль в панель (кнопка внизу, `/pass`) и уведомления; отвечает лишь вайтлисту |
| `rasp` | `ssau-rasp.service` | Парсер расписания `ssau.ru/rasp` + cron 7/15/19/22 |
| `panel` | `ssau-panel.service` | HTTPS Mini App + JSON API. Единственный, кто слушает порт |
| `bbb` | `ssau-bbb.service` | Гостевой join в BBB без браузера (HTTP + DDP + Pion WebRTC), запись, слайды, вейкворды |

Полный замысел, решения и этапы — [PLAN.md](PLAN.md). Устройство и потоки данных — [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md). Эксплуатация, конфиг, логи и деплой — [docs/OPERATIONS.md](docs/OPERATIONS.md). Проверка функций в тестовой комнате BBB — [docs/TESTING.md](docs/TESTING.md).

## Как это живёт

```
Telegram ──► tg ──┐
                  │
rasp cron ────────┼──► sqlite (WAL)
                  │
panel (Mini App) ─┤
                  │
bbb (DDP+WebRTC) ─┘
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

Для локальной разработки достаточно токена бота: парсер `rasp` ходит на публичную страницу без ключей, а `BBB_DRY_RUN=1` подменяет реальный BBB-клиент (`DirectJoiner`) заглушкой.

## Тесты

```bash
go test ./...                 # локально
CGO_ENABLED=0 go test ./...   # то же, что гоняет CI
go test ./... -cover          # покрытие по пакетам
go vet ./...
```

Юнит-тесты лежат рядом с кодом (`*_test.go`). Тесты не требуют сети: внешние API подменяются `httptest`, BBB-клиент — заглушками (`DryJoiner`).

## CI/CD

`.github/workflows/deploy.yml`:

- **pull_request → main**: только `CGO_ENABLED=0 go test ./...`.
- **push → main**: тесты → сборка `linux/amd64` четырёх бинарников → `scp` в `/opt/ssau-bot` → перезапуск systemd через `deploy/remote.sh`.
- **workflow_dispatch**: ручной прогон теста и деплоя.

Секреты репозитория: `VDS_SSH_KEY` (обязателен), `VDS_HOST`, `VDS_USER`, `VDS_PORT` (по умолчанию `203.0.113.20`, `root`, `22`; сейчас все четыре заданы). LLM для конспектов: `LLM_API_KEY/URL/MODEL`, без них — `DEEPSEEK_API_KEY` с `api.deepseek.com` и `deepseek-chat`. `.env` на сервере в деплой не входит — правится руками один раз.

## Прод

VDS прод-VDS (хостер, Прага, 4 CPU / 4 ГБ, Debian 13), `203.0.113.20`. Панель — `https://panel.example.com` за Caddy с сертификатом Let's Encrypt, открыта только IP из `PANEL_ALLOW_IPS` (выходы VPN-пула) и по `PANEL_PASSWORD`. Из РФ IP сервера напрямую не виден — панель открывается через VPN. Подробнее — [docs/OPERATIONS.md](docs/OPERATIONS.md#прод-прод-vds).

## Логи

Все процессы пишут через `internal/logx` с уровнями `debug/info/warn/error` в stderr и, если задан `LOG_FILE`, в общий файл. Подробности и рецепты диагностики — [docs/OPERATIONS.md](docs/OPERATIONS.md#логи).

```bash
journalctl -u ssau-bbb -f
LOG_LEVEL=debug   # в /opt/ssau-bot/.env для подробного разбора
```

## Риски

Посещаемость гостем и запись пары — серая зона правил универа и BBB. Репозиторий приватный. Не светить токены и записи.