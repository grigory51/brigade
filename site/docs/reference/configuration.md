# Конфигурация

Brigade читает YAML и применяет поверх него переменные окружения. Префикс — `BRIGADE_`, вложенность — двойное подчёркивание: `jwt.secret` превращается в `BRIGADE_JWT__SECRET`.

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
| `auth.oidc.client_secret` | `BRIGADE_AUTH__OIDC__CLIENT_SECRET` | Client secret приложения |
| `auth.oidc.redirect_url` | `BRIGADE_AUTH__OIDC__REDIRECT_URL` | Callback URL Brigade |
| `auth.oidc.required_role` | `BRIGADE_AUTH__OIDC__REQUIRED_ROLE` | Обязательная роль пользователя |
| `work_dir` | `BRIGADE_WORK_DIR` | Рабочие каталоги сессий |
| `agent_home_dir` | `BRIGADE_AGENT_HOME_DIR` | Персональные home агентов |
| `agent_image` | `BRIGADE_AGENT_IMAGE` | Базовый runtime-образ |
| `max_containers` | `BRIGADE_MAX_CONTAINERS` | Лимит контейнеров; `-1` отключает |
| `image_quota_bytes` | `BRIGADE_IMAGE_QUOTA_BYTES` | Квота пользовательских image layers |
| `memory.dir` | `BRIGADE_MEMORY__DIR` | Рабочие копии memory-репозиториев |
| `telegram.mode` | `BRIGADE_TELEGRAM__MODE` | `poll` или `webhook` |

`jwt.secret` должен оставаться стабильным: им зашифрованы секреты подключений агентов, MCP, notification connections и Telegram BotFather tokens.

OIDC настраивается по [отдельной инструкции](../guides/authentication.md). Отключайте
`auth.password_enabled` только после проверки входа через провайдера.

## TLS и reverse proxy

Можно включить встроенный TLS через `tls.addr`, `tls.cert_file` и `tls.key_file` либо поставить Brigade за reverse proxy. Прокси должен пропускать ConnectRPC, SSE и WebSocket без буферизации потоковых ответов.
