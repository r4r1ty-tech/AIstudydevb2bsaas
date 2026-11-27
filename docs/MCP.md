# MCP-сервер Moodle

`cmd/mcp` — MCP-сервер над Moodle `do.ssau.ru`. По умолчанию он только читает:
отдаёт студенту его курсы, структуру, материалы, сроки, файлы и локальные
конспекты лекций. Запись в тесты (`start`/`submit`/`finish`) выключена и
включается отдельной настройкой только с явным `confirm=true`. Работает по
stdio или Streamable HTTP, подключается к Claude Desktop, OpenCode, Cursor и
любому другому MCP-клиенту.

Документ описывает архитектуру, контракт инструментов, запуск, безопасность и
eval-прогон. Устройство остальных процессов — [ARCHITECTURE.md](ARCHITECTURE.md),
эксплуатация — [OPERATIONS.md](OPERATIONS.md).

## 1. Границы

- **Чтение по умолчанию.** `GET`-страницы, AJAX-чтение и скачивание файлов
  работают всегда. `startattempt`, `processattempt` и завершение попытки —
  только при `MOODLE_MCP_ALLOW_WRITES=1` и `confirm=true` у конкретного вызова.
- **Тесты.** Метаданные, страницы и разбор попыток читаются. Начать, отправить
  страницу и завершить попытку можно только осознанно: без `confirm=true`
  инструмент вернёт `confirmation_required`, без настройки — `writes_disabled`.
- **Один тест за раз.** Write-действия по одному `cmid` сериализуются: второй
  параллельный `start`/`submit`/`finish` получает `quiz_busy`.
- **Неподдерживаемые вопросы.** `essay`, drag&drop и другие неизвестные типы
  отклоняются кодом `unsupported_question` до отправки страницы.
- Пароль, куки и `sesskey` не попадают в ответы инструментов и в логи.
- Один аккаунт Moodle на процесс: первая строка `accounts` с `kind='moodle'`
  из `bot.db` либо пара `MOODLE_MCP_LOGIN`/`MOODLE_MCP_PASSWORD`.
- Внешние ссылки (`url`, `resource` на чужом хосте) не скачиваются: файловый
  allowlist ограничен хостом Moodle и `do.ssau.ru`.

## 2. Архитектура

```
cmd/mcp                 main: флаги, stdio/HTTP, сигналы, логи
internal/mcpapp         MCP-сервер: инструменты, ресурсы, промпты, сессия, кэш
internal/moodle         HTTP-клиент Moodle: вход, AJAX, страницы, файлы
internal/moodlematerial разбор модулей и извлечение текста (PDF/HTML)
internal/vault          расшифровка пароля и сессии из bot.db
```

- **Сессия.** `mcpapp.Session` держит один `moodle.Client` и общий мьютекс:
  вход, проверка кук и запросы не идут одновременно. Куки хранятся в
  `<state-dir>/session.json` с правами `0600`; протухшая сессия сбрасывается,
  выполняется один повторный вход и повтор запроса.
- **База.** `MOODLE_MCP_DB` открывается строго `mode=ro` с `query_only(1)`.
  Сервер не создаёт таблицы, не пишет миграции и не меняет `bot.db`.
- **Кэш.** Структура курса и поиск — 10 минут, материал — 24 часа, сроки —
  1 минута; до 128 записей на кэш. `refresh=true` в аргументах инструмента
  игнорирует кэш.
- **Параллельность.** Не больше 3 одновременных запросов к Moodle на процесс;
  инструменты сериализуются общим лимитером. Это защищает от банов и лишней
  нагрузки на университет.
- **Транспорты.** stdio — JSON-RPC построчно в stdin/stdout, логи только в
  stderr. HTTP — Streamable HTTP без серверных сессий, эндпоинт `/mcp`
  (настраивается), `/healthz` без аутентификации.

## 3. Инструменты

Все инструменты чтения помечены `readOnlyHint: true`. Write-инструменты теста
(`moodle_quiz_start`, `moodle_quiz_submit`, `moodle_quiz_finish`) помечены
`readOnlyHint: false`, выключены без `MOODLE_MCP_ALLOW_WRITES=1` и требуют
`confirm=true`. Ответы структурированные (`structuredContent`) и дублируются
текстом JSON для клиентов без поддержки structured output.

| Инструмент | Аргументы | Что возвращает |
| --- | --- | --- |
| `moodle_status` | — | `authenticated`, `login`, `base_url`, `last_refresh`, `warnings` |
| `moodle_courses` | `limit` (20, максимум 100) | `courses[]` (`id`, `name`, `shortname`, `hidden`, `url`), `total` |
| `moodle_course_outline` | `course_id`, `kind?`, `refresh?` | секции и модули: `cmid`, `kind`, `name`, `available`, ссылка; `total_modules`, `truncated` |
| `moodle_material_read` | `cmid`, `kind?`, `course_id?`, `max_chars?`, `include_files?`, `all_chapters?`, `refresh?` | текст материала, файлы, главы, статус/оценка/срок для заданий; `truncated`, `next` |
| `moodle_search` | `query`, `course_id?`, `kind?`, `deep?`, `refresh?` | `results[]` со сниппетами, `total`, `truncated`, `note` |
| `moodle_deadlines` | `days_ahead` (14), `include_overdue` (true), `course_id?`, `refresh?` | сроки в `Europe/Samara`: `due`, `opens`, `overdue`, `status`, `todo`, ссылка |
| `moodle_file_get` | `url?` или `cmid`, `course_id?`, `extract_text?` (true), `max_chars?`, `save?` | `filename`, `content_type`, `size`, `extracted`, `text`, `path` при `save=true` |
| `moodle_notes` | `action` (`list`/`read`), `id?`, `source?`, `discipline?`, `query?`, `limit?`, `max_chars?` | локальные конспекты: список лекций или текст выбранного источника |
| `moodle_quiz_info` | `cmid` | `can_start`, `in_progress_attempt_id`, правила, попытки, `allowed`, `used`, `attempts_left`, `grading_method` |
| `moodle_quiz_start` | `cmid`, `confirm` | `attempt_id`, `page`, `next_page`, `time_left`, `slots`, `questions`; продолжает незавершённую попытку |
| `moodle_quiz_page` | `cmid`, `attempt_id`, `page?` | страница попытки: вопросы, варианты, поля ввода, `slots`, `next_page`, `time_left` |
| `moodle_quiz_submit` | `cmid`, `attempt_id`, `page`, `answers`, `check_slot?`, `confirm` | `next_page`, `needs_finish`, `page`, обновлённые вопросы |
| `moodle_quiz_finish` | `cmid`, `attempt_id`, `confirm` | `status`, `grade`, `feedback`, `review_allowed`, результаты по вопросам |
| `moodle_quiz_review` | `cmid`, `attempt_id` | разбор попытки: ответы, правильные ответы, баллы, отметки; `review_closed`, если просмотр закрыт |
| `moodle_quiz_image` | `cmid`, `attempt_id`, `slot`, `index` | рисунок вопроса как image content или `path` в `StateDir/images`, `mime_type`, `size` |

Типы модулей в `kind`: `page`, `resource`, `folder`, `book`, `url`, `lesson`,
`assign`, `quiz`, `workshop`.

Особенности:

- `moodle_material_read` без `kind` ищет модуль по `cmid` в структуре курсов;
  с `course_id` делает это быстрее.
- Для папки `include_files=true` дополнительно читает текст первых файлов;
  без флага отдаёт список файлов.
- Для книги `all_chapters=true` обходит все главы.
- `moodle_file_get` принимает прямую ссылку `pluginfile.php` или `cmid`.
  Разрешены только `http(s)`, хост Moodle и `do.ssau.ru`, файл не больше
  25 МБ, редиректы на чужой хост отклоняются.
- `moodle_notes` читает `lecture_packs` из `bot.db` и каталог
  `MOODLE_MCP_RECORDINGS_DIR`. Пути за пределы каталога записей отклоняются.
- `moodle_quiz_submit` перед отправкой перечитывает страницу и проверяет слоты.
  Ответы: `choice` для multichoice с radio и truefalse, `choices` для чекбоксов,
  `text` для shortanswer и numerical, `matches` для match (ключ — имя `select`
  или текст строки). Неизвестный слот или вариант — `bad_input`; неподдерживаемый
  тип вопроса — `unsupported_question` без POST.
- `moodle_quiz_image` ищет слот по страницам попытки и скачивает рисунок через
  тот же allowlist, что `moodle_file_get` (хост Moodle и `do.ssau.ru`, не больше
  25 МБ). Небольшие PNG/JPEG/GIF/WebP возвращаются как image content, остальные —
  путём в `<state-dir>/images`.

## 4. Ресурсы

| URI | MIME | Содержимое |
| --- | --- | --- |
| `moodle://courses` | `application/json` | тот же список, что `moodle_courses` |
| `moodle://deadlines` | `application/json` | ближайшие сроки на 14 дней |
| `moodle://course/{id}/outline` | `application/json` | структура курса по id |
| `moodle://material/{cmid}` | `application/json` | материал по cmid |
| `moodle://notes/{id}/markdown` | `text/markdown` | конспект лекции; при отсутствии `notes.md` — текст из PDF с пометкой |
| `moodle://notes/{id}/pdf` | `application/pdf` | PDF-конспект, не больше 25 МБ |
| `moodle://quiz/{cmid}/info` | `application/json` | QuizInfo теста: правила, попытки, `can_start` |

Первые две записи — обычные ресурсы, остальные — шаблоны
(`resources/templates/list`). `id` конспекта берётся из `moodle_notes`.

## 5. Промпты

Промпты не вызывают LLM: они задают модели порядок инструментов, структуру
ответа и критерии готового результата.

| Промпт | Аргументы | Назначение |
| --- | --- | --- |
| `moodle_prepare_lecture` | `course`, `topic?`, `cmid?` | подготовка к лекции: материалы, файлы, сроки, готовые конспекты |
| `moodle_make_notes` | `course`, `topic?`, `cmid?` | сборка конспекта по материалам Moodle и локальным записям |
| `moodle_exam_review` | `course`, `topic?` | план повторения к зачёту или экзамену с приоритетами по срокам |
| `moodle_solve_quiz` | `cmid`, `course?`, `time_limit?` | прохождение теста: info → материалы → start → page/submit → finish → review, только по явному `confirm` |

## 6. Конфигурация

Переменные читаются из окружения, поверх них можно наложить JSON-файл
(`-config`). Пути с `~/` раскрываются от домашнего каталога.

| Переменная | По умолчанию | Назначение |
| --- | --- | --- |
| `MOODLE_MCP_BASE_URL` | `https://do.ssau.ru/moodle` | адрес Moodle |
| `MOODLE_MCP_STATE_DIR` | `~/.local/state/ssau-mcp` | состояние: `session.json`, логи, сохранённые файлы |
| `MOODLE_MCP_DB` | — | SQLite с аккаунтом Moodle; открывается только на чтение |
| `MOODLE_MCP_SECRET_KEY_FILE` | — | ключ vault для расшифровки пароля и сессии из базы |
| `MOODLE_MCP_RECORDINGS_DIR` | `recordings` | каталог записей, слайдов и конспектов |
| `MOODLE_MCP_LOGIN` | — | логин Moodle, если не берём из базы |
| `MOODLE_MCP_PASSWORD` | — | пароль Moodle; задавать вместе с логином |
| `MOODLE_MCP_TIMEOUT` | `40s` | deadline операции; `40s` или `40`; максимум 30 минут |
| `MOODLE_MCP_BEARER_TOKEN` | — | токен HTTP-транспорта; обязателен вне loopback |
| `MOODLE_MCP_ALLOW_WRITES` | `0` | `1` разрешает `moodle_quiz_start/submit/finish`; по умолчанию только чтение |

Секреты в репозиторий не кладём. JSON-конфиг с паролем должен иметь права
`0600`, иначе сервер откажется стартовать. Поля файла: `base_url`, `state_dir`,
`db`, `secret_key_file`, `recordings_dir`, `login`, `password`, `timeout`,
`allow_writes`.

## 7. Запуск

Сборка:

```bash
go build -o bin/mcp ./cmd/mcp
```

stdio (основной режим для десктопных клиентов):

```bash
MOODLE_MCP_DB=data/bot.db \
MOODLE_MCP_SECRET_KEY_FILE=data/secret.key \
./bin/mcp
```

HTTP:

```bash
MOODLE_MCP_DB=data/bot.db \
MOODLE_MCP_SECRET_KEY_FILE=data/secret.key \
MOODLE_MCP_BEARER_TOKEN='длинный-случайный-токен' \
./bin/mcp -transport http -http-addr 127.0.0.1:8090 -http-path /mcp
```

Проверка живости:

```bash
curl -fsS http://127.0.0.1:8090/healthz
```

Флаги:

| Флаг | По умолчанию | Назначение |
| --- | --- | --- |
| `-transport` | `stdio` | `stdio` или `http` |
| `-http-addr` | `127.0.0.1:8090` | адрес HTTP; вне loopback нужен Bearer |
| `-http-path` | `/mcp` | путь MCP-эндпоинта |
| `-config` | — | JSON-конфиг поверх env |
| `-state-dir` | из env | переопределить каталог состояния |
| `-recordings-dir` | из env | переопределить каталог записей и конспектов |

Логи идут в stderr; при `LOG_DIR` не заданном явно подробный лог пишется в
`<state-dir>/logs`. stdout в stdio-режиме занят JSON-RPC, поэтому `println` в
коде сервера недопустим.

## 8. Подключение клиентов

### Claude Desktop

`~/.config/Claude/claude_desktop_config.json` (Linux) или
`~/Library/Application Support/Claude/claude_desktop_config.json` (macOS):

```json
{
  "mcpServers": {
    "ssau-moodle": {
      "command": "/home/alex/dev/AIstudydevb2bsaas/bin/mcp",
      "env": {
        "MOODLE_MCP_DB": "/home/alex/dev/AIstudydevb2bsaas/data/bot.db",
        "MOODLE_MCP_SECRET_KEY_FILE": "/home/alex/dev/AIstudydevb2bsaas/data/secret.key",
        "MOODLE_MCP_STATE_DIR": "/home/alex/.local/state/ssau-mcp",
        "MOODLE_MCP_RECORDINGS_DIR": "/home/alex/dev/AIstudydevb2bsaas/recordings"
      }
    }
  }
}
```

### OpenCode

`~/.config/opencode/opencode.json`:

```json
{
  "$schema": "https://opencode.ai/config.json",
  "mcp": {
    "ssau-moodle": {
      "type": "local",
      "command": ["/home/alex/dev/AIstudydevb2bsaas/bin/mcp"],
      "enabled": true,
      "environment": {
        "MOODLE_MCP_DB": "/home/alex/dev/AIstudydevb2bsaas/data/bot.db",
        "MOODLE_MCP_SECRET_KEY_FILE": "/home/alex/dev/AIstudydevb2bsaas/data/secret.key",
        "MOODLE_MCP_RECORDINGS_DIR": "/home/alex/dev/AIstudydevb2bsaas/recordings"
      }
    }
  }
}
```

Для HTTP-режима:

```json
{
  "mcp": {
    "ssau-moodle": {
      "type": "remote",
      "url": "http://127.0.0.1:8090/mcp",
      "enabled": true,
      "headers": {"Authorization": "Bearer <токен>"}
    }
  }
}
```

### Cursor

`.cursor/mcp.json` в проекте или `~/.cursor/mcp.json`:

```json
{
  "mcpServers": {
    "ssau-moodle": {
      "command": "/home/alex/dev/AIstudydevb2bsaas/bin/mcp",
      "env": {
        "MOODLE_MCP_DB": "/home/alex/dev/AIstudydevb2bsaas/data/bot.db",
        "MOODLE_MCP_SECRET_KEY_FILE": "/home/alex/dev/AIstudydevb2bsaas/data/secret.key"
      }
    }
  }
}
```

### MCP Inspector

```bash
MOODLE_MCP_DB=data/bot.db MOODLE_MCP_SECRET_KEY_FILE=data/secret.key \
npx -y @modelcontextprotocol/inspector --cli --method tools/list -- ./bin/mcp
```

Для проверки обеих эпох протокола:

```bash
npx -y @modelcontextprotocol/inspector --cli --protocol-era legacy --method tools/list -- ./bin/mcp
npx -y @modelcontextprotocol/inspector --cli --protocol-era modern --method tools/list -- ./bin/mcp
```

## 9. Безопасность

- **Чтение по умолчанию.** Без `MOODLE_MCP_ALLOW_WRITES=1` write-инструменты
  теста возвращают `writes_disabled` и ничего не отправляют. При включённой
  записи каждый вызов `start`/`submit`/`finish` требует `confirm=true`.
- **Блокировка теста.** Write-действия по одному `cmid` не идут параллельно:
  второй вызов получает `quiz_busy`. Автоматических стартов из сроков или
  курсов нет.
- **Неподдерживаемые вопросы.** `essay`, drag&drop и другие неизвестные типы
  отклоняются до отправки, чтобы не потерять ответы и не сломать попытку.
- **Секреты.** Пароль и куки не возвращаются и не логируются. `session.json`
  и JSON-конфиг с паролем — `0600`.
- **База.** `bot.db` открывается `mode=ro` + `query_only(1)`.
- **Файлы.** Allowlist: хост Moodle и `do.ssau.ru`; только `http(s)`; потолок
  25 МБ; редиректы на чужой хост отклоняются.
- **Вывод.** Не больше 100 000 рун текста на ответ; `moodle_file_get` и
  материалы сообщают `truncated` и подсказку `next`.
- **HTTP.** По умолчанию только loopback. Вне loopback обязателен Bearer-токен
  (сравнение постоянного времени). Запросы с `Origin` допускаются только с
  localhost, `127.0.0.1`, `[::1]` и явно разрешённых источников.
- **Логи.** В stderr и `<state-dir>/logs`; тела ответов Moodle с секретами не
  пишутся. Для stdio stdout не засоряется.

Сервер видит личные данные студента (курсы, оценки, файлы). Доступ к нему
должен быть только у владельца аккаунта; HTTP-порт не публикуйте в интернет без
TLS-прокси и токена.

## 10. Evals

`evals/moodle_tasks.json` — 15 read-only задач: статус, курсы, поиск курса,
структура 1515/1742/1807, страница 62991, ресурс 63209, папка 62002, урок
60904, ссылка 59646, поиск «энтропия»/«Python», сроки на 7 и 30 дней,
извлечение PDF 63209, список конспектов, промпты/ресурсы. Каждая задача может
состоять из нескольких шагов; задача пройдена, только если прошли все шаги.
ID курсов и модулей привязаны к текущему семестру: после смены курсов задачи
нужно обновить.

`cmd/mcpeval` — Go-клиент MCP на `go-sdk v1.7.0`:
- stdio запускает `bin/mcp` как дочерний процесс;
- HTTP подключается к `/mcp` и добавляет Bearer;
- проверяет минимальный read-only набор инвариантов: 8 инструментов, 3 промпта,
  ресурсы и шаблоны. После T7 у сервера 15 инструментов и 4 промпта, но
  write-инструменты теста в обязательный read-only набор evals не входят;
- считает `passed/total`, `tool_calls`, `resource_reads`, `steps`,
  latency p50/p95/total, `output_bytes` и `est_tokens` (4 байта на токен);
- пишет JSON-отчёт, не сохраняя сырые ответы Moodle.

Живой прогон (база и ключ только на чтение):

```bash
go build -o bin/mcp ./cmd/mcp
go build -o bin/mcpeval ./cmd/mcpeval

MOODLE_MCP_DB=data/bot.db \
MOODLE_MCP_SECRET_KEY_FILE=data/secret.key \
MOODLE_MCP_STATE_DIR=/tmp/opencode/ssau-mcp-eval \
MOODLE_MCP_RECORDINGS_DIR=recordings \
./bin/mcpeval -tasks evals/moodle_tasks.json -out /tmp/opencode/mcpeval-report.json
```

HTTP:

```bash
./bin/mcpeval -transport http -url http://127.0.0.1:8090/mcp -bearer "$MOODLE_MCP_BEARER_TOKEN" \
  -tasks evals/moodle_tasks.json -out /tmp/opencode/mcpeval-http.json
```

Коды возврата: `0` — все задачи и инварианты прошли; `1` — есть провалы или
ошибка подключения. Отчёт содержит `summary`, `invariants`, `tasks` и
`limitations`. Сырые тексты материалов в отчёт не попадают.

На 27.11.2026 живой прогон: 15/15 задач, 21 шаг (включая чтение
`moodle://courses`), 17 вызовов инструментов, p50 ≈ 0,5 с, p95 ≈ 1,8 с, около
16 тыс. оценённых токенов. HTTP-прогон с Bearer дал те же 15/15.

## 11. Известные ограничения

- **HTTP-таймаут 40 с.** `internal/moodle.New` жёстко задаёт 40 секунд на
  HTTP-клиент. `MOODLE_MCP_TIMEOUT` больше 40 с работает только как deadline
  контекста и не расширяет клиент. Очень большие PDF и медленный Moodle могут
  не уложиться.
- **Конспекты локальные.** `moodle_notes` не ходит в Moodle: без `MOODLE_MCP_DB`
  с `lecture_packs` и каталога записей список будет пустым.
- **Тесты.** По умолчанию только чтение: `moodle_quiz_info`, `moodle_quiz_page`,
  `moodle_quiz_review` и `moodle_quiz_image` не меняют Moodle. Запись
  (`moodle_quiz_start/submit/finish`) включается `MOODLE_MCP_ALLOW_WRITES=1`,
  требует `confirm=true`, сериализуется по `cmid` и не поддерживает неизвестные
  типы вопросов. Автоматического решения тестов нет: модель должна сама
  прочитать материалы и заполнить ответы.
- **Один аккаунт.** Берётся первый Moodle-аккаунт из базы; переключения между
  студентами в одном процессе нет.
- **Кэш.** Структура и поиск могут отставать до 10 минут, материал — до суток.
  Используйте `refresh=true`, если нужны свежие данные.
- **Поиск.** `deep=true` читает текст только `page`/`book`/`resource` и не
  больше 60 материалов на курс; полнотекстового индекса нет.
- **Разметка Moodle.** Парсеры опираются на HTML/AJAX Moodle; после обновления
  университета возможны пустые поля. Проверяйте `warnings` в `moodle_status`.
- **HTTP-транспорт.** Stateless: серверных MCP-сессий нет, каждый запрос
  самостоятелен. `/healthz` открыт, `/mcp` защищён токеном при его задании.
