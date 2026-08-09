# Вход через OIDC

Brigade реализует OIDC Authorization Code Flow с PKCE. После callback он проверяет
подпись, issuer, audience, срок действия и nonce ID token, затем обязательную роль.
При успехе создаёт или находит пользователя по неизменяемой паре `issuer` + `sub` и
выдаёт локальную JWT-сессию Brigade в httpOnly cookies. Успешная аутентификация у
провайдера без `required_role` не даёт доступ к Brigade.

Ниже — production-настройка для ZITADEL. Не отключайте парольный вход до её проверки.

## Настройка ZITADEL Console

1. Создайте или выберите **Project** для Brigade.
2. В Project откройте **Roles**, создайте ключ роли `brigade:user` и сохраните его.
3. Откройте **Role Assignments**, назначьте `brigade:user` каждому пользователю или
   группе, которой нужен доступ.
4. В Project создайте **Web application**. Включите **Authorization Code Flow** и PKCE.
5. Добавьте ровно такой redirect URI, подставив публичное имя Brigade:

   ```text
   https://<brigade>/auth/oidc/callback
   ```

6. Скопируйте **Client ID**. Если выбранный способ аутентификации создал
   **Client secret**, также сохраните его; для PKCE-only приложения `client_secret`
   оставьте пустым.
7. В настройках Project включите **Assert Roles on Authentication** или в Token Settings
   приложения — **User Roles Inside ID Token**. Brigade также явно запрашивает нужную
   роль через scope и при отсутствии claim в ID token проверяет UserInfo.

Brigade запрашивает scope
`urn:zitadel:iam:org:project:role:brigade:user`. Он запрашивает claim
`urn:zitadel:iam:org:project:roles` и ищет в нём ключ `brigade:user`.

`issuer` — публичный HTTPS origin ZITADEL, например `https://auth.example.com`. Не
указывайте `/.well-known/openid-configuration`, `/oauth` или другой путь. До включения
Brigade убедитесь, что discovery доступен:

```bash
curl -fsS https://auth.example.com/.well-known/openid-configuration
```

Ответ должен быть JSON с `issuer`, совпадающим с настроенным origin.

## Конфигурация

Полный YAML-пример. Client secret и JWT-secret в production задавайте через окружение,
а не храните в файле.

```yaml
addr: ":8080"
sqlite_path: "/srv/brigade/brigade.db"

jwt:
  secret: "replace-with-a-stable-random-secret"
  access_ttl: "15m"
  refresh_ttl: "720h"

auth:
  password_enabled: true
  oidc:
    issuer: "https://auth.example.com"
    client_id: "<ZITADEL_CLIENT_ID>"
    client_secret: "<ZITADEL_CLIENT_SECRET>" # пусто для PKCE-only приложения
    redirect_url: "https://brigade.example.com/auth/oidc/callback"
    name: "ZITADEL"
    required_role: "brigade:user"
    role_claim: "urn:zitadel:iam:org:project:roles"
    username_claim: "name"
    scopes:
      - "openid"
      - "profile"
      - "email"
      - "urn:zitadel:iam:org:project:role:brigade:user"

seed:
  username: "admin"
  password: "<INITIAL_ADMIN_PASSWORD>"

work_dir: "/srv/brigade/workspace"
```

В Docker-образе уже есть YAML со значениями по умолчанию; ниже — полный набор env
override для OIDC-установки. `scopes` можно не задавать: для ZITADEL Brigade применяет
значения из YAML-примера.

```dotenv
BRIGADE_MODE=docker
BRIGADE_SQLITE_PATH=/srv/brigade/brigade.db
BRIGADE_WORK_DIR=/srv/brigade/workspace
BRIGADE_JWT__SECRET=<STABLE_RANDOM_SECRET>
BRIGADE_SEED__USERNAME=admin
BRIGADE_SEED__PASSWORD=<INITIAL_ADMIN_PASSWORD>
BRIGADE_AUTH__PASSWORD_ENABLED=true
BRIGADE_AUTH__OIDC__ISSUER=https://auth.example.com
BRIGADE_AUTH__OIDC__CLIENT_ID=<ZITADEL_CLIENT_ID>
# Не задавайте для PKCE-only приложения.
BRIGADE_AUTH__OIDC__CLIENT_SECRET=<ZITADEL_CLIENT_SECRET>
BRIGADE_AUTH__OIDC__REDIRECT_URL=https://brigade.example.com/auth/oidc/callback
BRIGADE_AUTH__OIDC__NAME=ZITADEL
BRIGADE_AUTH__OIDC__REQUIRED_ROLE=brigade:user
BRIGADE_AUTH__OIDC__ROLE_CLAIM=urn:zitadel:iam:org:project:roles
BRIGADE_AUTH__OIDC__USERNAME_CLAIM=name
```

## Безопасное включение

1. Примените конфигурацию с `auth.password_enabled: true` и перезапустите Brigade.
2. В отдельном browser profile или приватном окне нажмите **Войти через ZITADEL**.
3. Проверьте вход пользователя с `brigade:user` и отказ пользователя без этой роли.
4. Только после этого задайте `auth.password_enabled: false` и перезапустите Brigade.

При отключённом password login `seed.username` и `seed.password` не обязательны. Не
меняйте `jwt.secret`: им подписаны Brigade JWT и зашифрованы сохранённые секреты.

## Перенос существующего пользователя

Первый вход через OIDC создаёт отдельного пользователя: Brigade намеренно не связывает
аккаунты по совпавшему username. Чтобы перенести существующие сессии и настройки в уже
созданного OIDC-пользователя, сначала сделайте резервную копию, затем выполните:

```bash
docker exec brigade brigade user list
docker exec brigade brigade user migrate <OLD_USER_ID> <OIDC_USER_ID>
docker restart brigade
```

Команда сохраняет целевую OIDC identity, переносит строки БД, память и agent home,
отзывает refresh tokens старого пользователя и удаляет его. У целевого пользователя не
должно быть сессий. Если его каталог памяти или agent home уже существует, команда
остановится до изменения БД.

Лишнего пользователя без сессий можно удалить отдельно:

```bash
docker exec brigade brigade user delete <USER_ID> --yes
docker restart brigade
```

## Docker и reverse proxy

Публичный HTTPS-адрес из `redirect_url` должен быть доступен браузеру пользователя.
Контейнер Brigade должен иметь исходящий HTTPS-доступ к конечным точкам discovery,
token и UserInfo. Если TLS завершает reverse proxy, проксируйте `/auth/oidc/start` и
`/auth/oidc/callback` на Brigade без переписывания пути и query string. Для всего UI
также пропускайте ConnectRPC, SSE и WebSocket; у streaming-ответов не включайте
буферизацию. В Docker оставьте тот же публичный URL в ZITADEL и
`BRIGADE_AUTH__OIDC__REDIRECT_URL`, а наружный `:443` проксируйте на порт Brigade.

Brigade помечает cookies как `Secure`, когда `redirect_url` начинается с `https://`.
Не используйте HTTP на публичном инстансе. Callback с `http://127.0.0.1` разрешён только
для локальной разработки.

## Диагностика

Смотрите логи сервера во время входа:

```bash
docker logs brigade
```

| Симптом | Что проверить |
| --- | --- |
| Сервер не запускается: `brigade: OIDC: auth: OIDC discovery: ...` | URL `issuer`, DNS, TLS и ответ `/.well-known/openid-configuration`. В `issuer` не должно быть пути discovery или `/oauth`. |
| После callback браузер вернулся на `/login?error=oidc` | В логах найдите `auth: complete OIDC:`. Частые причины: redirect URI отличается хотя бы символом, Client secret неверен, token не содержит `id_token` или не проходит проверку issuer/audience/nonce. |
| В логах `auth: complete OIDC: auth: required OIDC role is missing` | Назначьте `brigade:user` через ZITADEL Role Assignments, включите assertion ролей и заново войдите, чтобы провайдер выдал новый token. |
| Вход прошёл у провайдера, но сессии нет | Проверьте, что callback приходит на тот же публичный HTTPS origin, а proxy не удаляет `Set-Cookie`. |

## Brigade.app и удалённые окружения

Для удалённого окружения Brigade.app открывает тот же серверный `/auth/oidc/start`,
после входа получает одноразовый код на loopback callback приложения и обменивает его на
локальную Brigade JWT-сессию. Refresh token хранится в macOS Keychain. Никакой отдельный
redirect URI ZITADEL для Brigade.app не нужен: в ZITADEL остаётся
`https://<brigade>/auth/oidc/callback`.

## Другой OIDC-провайдер

Задайте подходящие `role_claim`, `username_claim` и `scopes`; `scopes` обязан содержать
`openid`. `required_role` обязателен для любого провайдера.
