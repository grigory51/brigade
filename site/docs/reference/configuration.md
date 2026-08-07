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
| `work_dir` | `BRIGADE_WORK_DIR` | Рабочие каталоги сессий |
| `agent_home_dir` | `BRIGADE_AGENT_HOME_DIR` | Персональные home агентов |
| `agent_image` | `BRIGADE_AGENT_IMAGE` | Базовый runtime-образ |
| `max_containers` | `BRIGADE_MAX_CONTAINERS` | Лимит контейнеров; `-1` отключает |
| `image_quota_bytes` | `BRIGADE_IMAGE_QUOTA_BYTES` | Квота пользовательских image layers |
| `memory.dir` | `BRIGADE_MEMORY__DIR` | Рабочие копии memory-репозиториев |
| `telegram.mode` | `BRIGADE_TELEGRAM__MODE` | `poll` или `webhook` |

`jwt.secret` должен оставаться стабильным: им зашифрованы agent tokens, MCP secrets, notification tokens и Telegram BotFather tokens.

## TLS и reverse proxy

Можно включить встроенный TLS через `tls.addr`, `tls.cert_file` и `tls.key_file` либо поставить Brigade за reverse proxy. Прокси должен пропускать ConnectRPC, SSE и WebSocket без буферизации потоковых ответов.
