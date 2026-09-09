# Конфигурация

Brigade читает YAML и применяет поверх него переменные окружения. Префикс — `BRIGADE_`, вложенность — двойное подчёркивание: `jwt.secret` превращается в `BRIGADE_JWT__SECRET`.
Путь к YAML задаётся флагом `--config` или переменной `BRIGADE_CONFIG`; флаг имеет
приоритет. В Docker-образе `BRIGADE_CONFIG=/etc/brigade/config.yaml` задан автоматически.

Полный аннотированный пример находится в [`backend/config.example.yaml`](https://github.com/grigory51/brigade/blob/main/backend/config.example.yaml).

## Основные параметры

| YAML | Env | Назначение |
| --- | --- | --- |
| `mode` | `BRIGADE_MODE` | `local` или `docker` для всего инстанса |
| `addr` | `BRIGADE_ADDR` | HTTP listener |
| `sqlite_path` | `BRIGADE_SQLITE_PATH` | База данных |
| `jwt.secret` | `BRIGADE_JWT__SECRET` | Подпись JWT и ключ шифрования секретов |
| `auth.password_enabled` | `BRIGADE_AUTH__PASSWORD_ENABLED` | Показывать вход по логину и паролю |
| `auth.oidc.issuer` | `BRIGADE_AUTH__OIDC__ISSUER` | OIDC issuer; пустое значение отключает OIDC |
| `auth.oidc.client_id` | `BRIGADE_AUTH__OIDC__CLIENT_ID` | Client ID приложения |
| `auth.oidc.client_secret` | `BRIGADE_AUTH__OIDC__CLIENT_SECRET` | Client secret приложения; необязателен для PKCE-only |
| `auth.oidc.redirect_url` | `BRIGADE_AUTH__OIDC__REDIRECT_URL` | Callback URL Brigade |
| `auth.oidc.required_role` | `BRIGADE_AUTH__OIDC__REQUIRED_ROLE` | Обязательная роль пользователя |
| `auth.oidc.role_claim` | `BRIGADE_AUTH__OIDC__ROLE_CLAIM` | Claim с ролями; по умолчанию ZITADEL claim |
| `auth.oidc.username_claim` | `BRIGADE_AUTH__OIDC__USERNAME_CLAIM` | Claim для имени пользователя; по умолчанию `name` |
| `auth.oidc.scopes` | — | OAuth scopes; без значения используются ZITADEL defaults |
| `work_dir` | `BRIGADE_WORK_DIR` | Рабочие каталоги сессий |
| `agent_home_dir` | `BRIGADE_AGENT_HOME_DIR` | Персональные home агентов |
| `agent_image` | `BRIGADE_AGENT_IMAGE` | Базовый runtime-образ и основа сборки окружения из скрипта |
| `max_containers` | `BRIGADE_MAX_CONTAINERS` | Лимит контейнеров; `-1` отключает |
| `image_quota_bytes` | `BRIGADE_IMAGE_QUOTA_BYTES` | Квота layers принятых пользовательских образов; не жёсткий лимит диска для сборки или build cache |
| `memory.dir` | `BRIGADE_MEMORY__DIR` | Рабочие копии memory-репозиториев |
| `plugins_dir` | `BRIGADE_PLUGINS_DIR` | Каталог установленных MCPB-плагинов |
| `telegram.mode` | `BRIGADE_TELEGRAM__MODE` | `poll` или `webhook` |

`jwt.secret` должен оставаться стабильным: им зашифрованы секреты подключений агентов, MCP, notification connections и Telegram BotFather tokens.

OIDC настраивается по [отдельной инструкции](../guides/authentication.md). Для ZITADEL
`issuer` — публичный HTTPS origin инстанса, а не URL discovery. Для другого провайдера
используйте точное значение `issuer` из discovery. Отключайте
`auth.password_enabled` только после проверки входа через провайдера.

## Сборка окружения

| YAML | Env | По умолчанию | Назначение |
| --- | --- | --- | --- |
| `image_build.backend` | `BRIGADE_IMAGE_BUILD__BACKEND` | `docker` | Backend сборки; единственное поддерживаемое значение — `docker` |
| `image_build.timeout` | `BRIGADE_IMAGE_BUILD__TIMEOUT` | `30m` | Общий таймаут заявки, включая ожидание в очереди |
| `image_build.max_concurrent` | `BRIGADE_IMAGE_BUILD__MAX_CONCURRENT` | `2` | Максимальное число одновременно выполняемых сборок |

Общее число активных заявок, включая выполняемые и ожидающие, ограничено `max_concurrent * 4` (по умолчанию — 8). На одного пользователя допускается одна активная заявка.

| Свойство | Поведение |
| --- | --- |
| Скрипт | Непустой, не более 64 KiB |
| Лог | Доступны последние 256 KiB |
| Базовый образ | Значение `agent_image` в конфигурации |
| Пользователь сборки | `USER root` при выполнении скрипта в `RUN`; затем `USER 1001:1001` |
| Runtime | Сборка через Docker API создаёт образ, не изменяя подключаемые runtime-компоненты Brigade и работающие сессии |
| Секреты | Vault не передаётся в сборку; скрипт не предназначен для хранения секретов |
| Состояние | Сохраняется при обновлении страницы и перезапуске сервера; незавершённая сборка после перезапуска помечается прерванной, без автоматического возобновления |
| Результат | Старые образы и сессии сохраняются; новый образ пользователь выбирает при создании сессии |
| Brigade.app | Используется выбранный работающий Docker context; если его изменение требует перезапуска, сборка недоступна до перезапуска приложения |

Docker backend предназначен для доверенных пользователей. Сборка пользовательского скрипта не является безопасной sandbox для недоверенного кода. `image_quota_bytes` учитывает только принятые образы и не ограничивает жёстко место, занятое промежуточными слоями и build cache.

Шаги сборки окружения из скрипта приведены в [инструкции по среде агента](../guides/environment.md#пользовательские-образы).

## TLS и reverse proxy

Можно включить встроенный TLS через `tls.addr`, `tls.cert_file` и `tls.key_file` либо поставить Brigade за reverse proxy. Прокси должен пропускать ConnectRPC, SSE и WebSocket без буферизации потоковых ответов.
