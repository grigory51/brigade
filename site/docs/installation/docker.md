# Установка в Docker

Docker — рекомендуемый режим для сервера: Brigade работает в одном контейнере, а каждую ACP-сессию запускает в отдельном контейнере агента.

## Автоматическая установка

```bash
curl -fsSL https://grigory51.github.io/brigade/install.sh | sudo sh
```

Переменные установки:

| Переменная | По умолчанию | Назначение |
| --- | --- | --- |
| `BRIGADE_DATA_DIR` | `/srv/brigade` | Данные, workspace и home агентов |
| `BRIGADE_PORT` | `8080` | Порт веб-интерфейса |
| `BRIGADE_VERSION` | `latest` | Тег серверного образа |
| `BRIGADE_ADMIN_PASSWORD` | генерируется | Начальный пароль `admin` |
| `BRIGADE_CONTAINER_NAME` | `brigade` | Имя контейнера |

Пример с фиксированной версией и паролем:

```bash
curl -fsSL https://grigory51.github.io/brigade/install.sh | \
  sudo BRIGADE_VERSION=0.46.2 BRIGADE_ADMIN_PASSWORD='change-this' sh
```

## Ручной запуск

```bash
mkdir -p /srv/brigade/{workspace,agent-home,memory,plugins}

docker run -d --name brigade --restart unless-stopped \
  -p 8080:8080 \
  -v /var/run/docker.sock:/var/run/docker.sock \
  -v /srv/brigade:/srv/brigade \
  -e BRIGADE_MODE=docker \
  -e BRIGADE_SQLITE_PATH=/srv/brigade/brigade.db \
  -e BRIGADE_WORK_DIR=/srv/brigade/workspace \
  -e BRIGADE_AGENT_HOME_DIR=/srv/brigade/agent-home \
  -e BRIGADE_MEMORY__DIR=/srv/brigade/memory \
  -e BRIGADE_PLUGINS_DIR=/srv/brigade/plugins \
  -e BRIGADE_AGENT_IMAGE=ghcr.io/grigory51/brigade-agent:latest \
  -e BRIGADE_JWT__SECRET='replace-with-a-random-secret' \
  -e BRIGADE_SEED__PASSWORD='replace-this-password' \
  ghcr.io/grigory51/brigade:latest
```

Путь данных должен совпадать внутри и снаружи серверного контейнера: bind mount рабочих каталогов выполняет Docker daemon хоста.

## Свой образ агента

Базовый образ — `ghcr.io/grigory51/brigade-agent:latest`. Пользовательские образы добавляются в **Настройки → Среда агента**. Brigade копирует в них runtime-слой из базового образа, поэтому заранее устанавливайте только нужные языки и инструменты.
