# Обновление

## Docker-установка

Секреты и пути находятся в `/srv/brigade/brigade.env`, данные — в `/srv/brigade`. Сохраните этот файл: смена `BRIGADE_JWT__SECRET` сделает зашифрованные секреты нечитаемыми.

```bash
docker pull ghcr.io/grigory51/brigade:latest
docker stop brigade
docker rm brigade
docker run -d \
  --name brigade \
  --restart unless-stopped \
  -p 8080:8080 \
  -v /var/run/docker.sock:/var/run/docker.sock \
  -v /srv/brigade:/srv/brigade \
  --env-file /srv/brigade/brigade.env \
  ghcr.io/grigory51/brigade:latest
```

Миграции SQLite выполняются автоматически при старте. Старые одиночные настройки Claude и Codex автоматически превращаются в именованные подключения агентов. Образ агента Brigade подтянет при создании или полной перезагрузке сессии.

## Бинарь и исходники

Замените бинарь архивом из нового GitHub Release, не меняя `config.yaml` и каталоги данных. Для исходников обновите репозиторий и повторите `make build`.

Перед обновлением сделайте [резервную копию](../reference/backup.md) и прочитайте release notes.
