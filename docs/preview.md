# Preview: публикация dev-серверов сессий

Агент внутри сессии поднимает dev-сервер — brigade делает его доступным по URL вида
`{scheme}://{sessionId}-{port}.{domain}`. Роутинг выполняет встроенный L7-прокси
brigade (никаких внешних reverse proxy не требуется): порт и сессия кодируются в
одном поддомен-лейбле, маршрут выводится из hostname детерминированно и работает
для любого порта без регистрации. WebSocket (HMR) и SSE проксируются.

Upstream по режиму сессии:

- **local** — `127.0.0.1:{port}` хоста brigade;
- **docker** — `{container-ip}:{port}` в bridge-сети (порты контейнера не
  публикуются). Dev-сервер в контейнере обязан слушать `0.0.0.0`.

## Как это видит агент

При создании сессии brigade кладёт в рабочую директорию скилл
`.claude/skills/brigade-preview/SKILL.md` (существующий не перезаписывается) и
передаёт агенту переменные окружения:

| Переменная | Значение |
|---|---|
| `BRIGADE_SESSION_ID` | идентификатор сессии |
| `BRIGADE_PREVIEW_TOKEN` | HMAC-токен регистрации (scoped на сессию) |
| `BRIGADE_API_URL` | адрес API brigade (`127.0.0.1` / `host.docker.internal`) |
| `BRIGADE_PREVIEW_URL_TEMPLATE` | шаблон публичного URL с плейсхолдером `{port}` |

Регистрация (только для появления ссылки в UI — прокси работает и без неё):

```sh
curl -X POST "$BRIGADE_API_URL/api/preview/$BRIGADE_SESSION_ID/register" \
  -H "Authorization: Bearer $BRIGADE_PREVIEW_TOKEN" \
  -H "Content-Type: application/json" -d '{"port": 3000, "name": "vite"}'
```

Ссылки показываются в нижнем баре сессии. Реестр регистраций живёт в памяти:
после рестарта brigade ссылка из UI пропадает (URL продолжает работать),
повторный curl возвращает её.

## Локальная разработка

```yaml
preview:
  enabled: true
  domain: "localhost"
  scheme: "http"
```

URL: `http://{sessionId}-3000.localhost:10000`. Chrome и Firefox резолвят
`*.localhost` в 127.0.0.1 сами; curl требует `--resolve`, Safari — не гарантированно.

## Прод: wildcard-домен + встроенный TLS

1. DNS: `brigade.example.com` и `*.brigade.example.com` → IP хоста brigade.
2. Wildcard-сертификат c SAN `brigade.example.com` **и** `*.brigade.example.com`
   (wildcard сам по себе корень не покрывает). Let's Encrypt выдаёт wildcard только
   по DNS-01: `certbot certonly --preferred-challenges dns -d brigade.example.com
   -d '*.brigade.example.com'`.
3. Конфиг:

```yaml
addr: ":10000"        # plain: локальный доступ и API регистрации для агентов
tls:
  addr: ":443"
  cert_file: "/etc/letsencrypt/live/brigade.example.com/fullchain.pem"
  key_file: "/etc/letsencrypt/live/brigade.example.com/privkey.pem"
preview:
  enabled: true
  domain: "brigade.example.com"
  scheme: "https"
```

Внешний TLS-терминатор (nginx/Caddy/Traefik) не нужен, но возможен: направьте
`*.domain` и `domain` на plain-порт brigade и задайте `preview.external_port: 443`.

## Ограничения

- **Preview-ссылки публичны**: кто знает URL — тот открыл. Рассчитано на закрытую
  сеть/VPN; авторизация на поддоменах — возможное развитие.
- **Docker Desktop (macOS/Windows)**: хост не видит bridge-IP контейнеров —
  docker-preview не работает. Linux — работает из коробки. **OrbStack** — работает
  при включённом network bridge: `orb config set network_bridge true` (+ рестарт
  движка), иначе container IP с хоста недоступен.
- В local-режиме прокси открывает доступ к любому 127.0.0.1-порту хоста при
  валидной running-сессии (следствие детерминированного роутинга); ужесточение до
  «только зарегистрированных портов» — возможная опция.
