# SSAU lecture bot

Четыре бинарника на Go, один VDS, общая sqlite. Не монолит.

| Бинарник | Unit | Зачем |
| --- | --- | --- |
| `tg` | `ssau-tg.service` | Telegram: только личный пароль в панель (кнопка внизу, `/pass`) и уведомления; отвечает лишь вайтлисту |
| `rasp` | `ssau-rasp.service` | Расписание (`ssau.ru/rasp` или ЛК), синк ЛК и Moodle, ФИО из ЛК, авторешение тестов Moodle через ИИ |
| `panel` | `ssau-panel.service` | Mini App + JSON API: пары и решения, «Сдать» с кнопкой «Пройти», конспекты, аккаунт универа, выбор модели ИИ, «Служебное» админу. Единственный, кто слушает порт |
| `bbb` | `ssau-bbb.service` | Гостевой join в BBB без браузера (HTTP + DDP + Pion WebRTC), запись, слайды, вейкворды |

Полный замысел, решения и этапы — [PLAN.md](PLAN.md). Устройство и потоки данных — [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md). Эксплуатация, конфиг, логи и деплой — [docs/OPERATIONS.md](docs/OPERATIONS.md). Проверка функций в тестовой комнате BBB — [docs/TESTING.md](docs/TESTING.md). Устройство тестов Moodle — [docs/MOODLE_QUIZ.md](docs/MOODLE_QUIZ.md).

Кто чем пользуется: бот — только пароль в панель и уведомления; всё остальное — в панели. Вход в панель — личный пароль на час из бота (у каждого свой, по нему панель знает, кто вошёл).

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

Связка — sqlite, не gRPC. Уведомления `bbb` и `rasp` шлют в Telegram сами (`internal/notify`), не через процесс `tg`. `ssau.target` поднимает все четыре. Папки `internal/*` — общая библиотека, в проде сами по себе не бегают.

## Быстрый старт

```bash
cp env.example .env      # вписать TELEGRAM_BOT_TOKEN, PANEL_PASSWORD, ключи ИИ (GEMINI_API_KEYS)
go build -o bin/tg ./cmd/tg
go build -o bin/rasp ./cmd/rasp
go build -o bin/panel ./cmd/panel
go build -o bin/bbb ./cmd/bbb
```

Для локальной разработки достаточно токена бота: парсер `rasp` ходит на публичную страницу без ключей, а `BBB_DRY_RUN=1` подменяет реальный BBB-клиент (`DirectJoiner`) заглушкой. Панель без базы и бота: `PANEL_DEMO=1 LISTEN_ADDR=127.0.0.1:18080 go run ./cmd/panel`.

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
- **push → main**: тесты → сборка `linux/amd64` четырёх бинарников → `scp` в `/tmp/ssau-deploy` → `deploy/remote.sh` ставит в `/opt/ssau-bot` и перезапускает systemd.
- **workflow_dispatch**: ручной прогон теста и деплоя с любой ветки: `gh workflow run deploy.yml --ref webui`.

Секреты репозитория: `VDS_SSH_KEY` (обязателен), `VDS_HOST`, `VDS_USER`, `VDS_PORT` (по умолчанию `13.143.234.228`, `root`, `22`; сейчас все четыре заданы). `LLM_API_KEY/URL/MODEL` — ключ ИИ только для админа (ставит `ssau-keys`), без них — `DEEPSEEK_API_KEY` с `api.deepseek.com` и `deepseek-chat`; `LECTURES_TOKEN` — выгрузка конспектов в GitHub. Бесплатные ключи ИИ (`GEMINI_API_KEYS`, `OPENCODE_API_KEYS`) и остальное — в `.env` на сервере, в деплой не входит.

Смена ключа ИИ админа и токена бота на проде — `go run ./cmd/ssau-keys` (или ярлык «SSAU ключи»): спросит, проверит, запишет в `.env` на сервере и в секреты GitHub.

## Прод

VDS ГлебПрод (OneDash, Прага, 4 CPU / 4 ГБ, Debian 13), `13.143.234.228`. Панель — `https://ssau.r4r1ty.fun` за Caddy с сертификатом Let's Encrypt, открыта только IP из `PANEL_ALLOW_IPS` (выходы VPN-пула), вход — личным паролем из бота (или общим `PANEL_PASSWORD` админом). Из РФ IP сервера напрямую не виден — панель открывается через VPN. Подробнее — [docs/OPERATIONS.md](docs/OPERATIONS.md#прод-глебпрод).

## Логи

Все процессы пишут через `internal/logx` с уровнями `debug/info/warn/error` в stderr и, если задан `LOG_FILE`, в общий файл. Подробности и рецепты диагностики — [docs/OPERATIONS.md](docs/OPERATIONS.md#логи).

```bash
journalctl -u ssau-bbb -f
LOG_LEVEL=debug   # в /opt/ssau-bot/.env для подробного разбора
```

## Риски

Посещаемость гостем и запись пары — серая зона правил универа и BBB. Репозиторий приватный. Не светить токены и записи.