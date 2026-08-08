# Вход через OIDC

Brigade поддерживает Authorization Code Flow с PKCE. Пользователь создаётся при первом
успешном входе и затем связывается с неизменяемой парой `issuer` + `sub`.

## ZITADEL

1. Создайте Web application и добавьте redirect URI
   `https://brigade.example.com/auth/oidc/callback`.
2. Создайте project role `brigade:user`, назначьте её пользователям и включите передачу
   ролей в ID token либо UserInfo.
3. Настройте Brigade:

```yaml
auth:
  password_enabled: true
  oidc:
    issuer: "https://auth.example.com"
    client_id: "..."
    client_secret: "..."
    redirect_url: "https://brigade.example.com/auth/oidc/callback"
    name: "ZITADEL"
    required_role: "brigade:user"
```

Brigade проверяет подпись, issuer, audience, срок ID token, nonce и наличие роли. Для
ZITADEL по умолчанию используются claim `urn:zitadel:iam:org:project:roles` и scope
`urn:zitadel:iam:org:project:role:brigade:user`.

Сначала проверьте вход через кнопку **Войти через ZITADEL**. После этого можно убрать
парольную форму:

```yaml
auth:
  password_enabled: false
```

При отключённом password login поля `seed.username` и `seed.password` необязательны.
Brigade.app использует тот же OIDC-поток для удалённых окружений; токены Brigade после
callback сохраняются в macOS Keychain.

## Другой OIDC-провайдер

Переопределите `role_claim`, `username_claim` и `scopes`. `required_role` обязателен:
пользователь без этой роли не войдёт, даже если провайдер успешно его аутентифицировал.
