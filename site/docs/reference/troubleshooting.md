# Диагностика

## Сессия не стартует в Docker

Проверьте сервер и контейнеры:

```bash
docker logs brigade
docker ps -a --filter label=brigade.session.id
docker pull ghcr.io/grigory51/brigade-agent:latest
```

Если пользовательский image не содержит runtime-слоя Brigade, обновите базовый `agent_image` и выполните полную перезагрузку сессии.

## Агент не видит команду

`stdio` MCP и команды агента выполняются внутри среды сессии, а не в серверном контейнере. Добавьте инструмент в пользовательский image и выберите его для сессии.

## Секреты перестали читаться

Проверьте, не изменился ли `BRIGADE_JWT__SECRET`. Он одновременно служит ключом шифрования. Верните значение из backup.

## Telegram не получает updates

- для `poll` проверьте, что тот же BotFather token не опрашивает другой процесс;
- для `webhook` проверьте публичный HTTPS URL и логи Brigade;
- повторно создайте ссылку привязки и убедитесь, что бот связан с владельцем.

## Диагностический dump сессии

Команда собирает версию, состояние Brigade и agent runtime, историю ACP и доступные контейнерные логи:

```bash
brigade dump debug session <session-id> --config /etc/brigade/config.yaml
```

В контейнерной установке:

```bash
docker exec brigade brigade dump debug session <session-id> --config /etc/brigade/config.yaml
```

Передавайте dump вместе с точным симптомом. Секретные значения в диагностике не должны выводиться.
