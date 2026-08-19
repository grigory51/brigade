# Плагины

Плагин превращает ACP-сессию в предметный workspace: его MCP-сервер даёт агенту
инструменты, а MCP App занимает верхнюю часть страницы над обычным чатом. Установка
выполняется оператором инстанса; пользователи выбирают установленный интерфейс при
создании сессии.

## Установка

Скачайте `.mcpb` для среды исполнения с [последнего релиза Brigade](https://github.com/grigory51/brigade/releases/latest).
Для Docker-сессий нужен Linux bundle, для local-сессий Brigade.app — macOS bundle.

```bash
brigade plugin install ./brigade-cad-linux-amd64.mcpb
brigade plugin list
```

Для Brigade в Docker команда выполняется внутри серверного контейнера:

```bash
docker exec brigade brigade plugin install \
  https://github.com/grigory51/brigade/releases/latest/download/brigade-cad-linux-amd64.mcpb
```

После установки откройте **Новая сессия → Интерфейс** и выберите плагин. Версия
закрепляется за сессией: обновление плагина влияет только на новые сессии.

В Brigade.app укажите конфиг приложения явно:

```bash
BRIGADE_PLUGINS_DIR="$HOME/Library/Application Support/Brigade/plugins" \
  "/Applications/Brigade.app/Contents/MacOS/Brigade" \
  --config "$HOME/Library/Application Support/Brigade/config.yaml" \
  plugin install ./brigade-cad-darwin-arm64.mcpb
```

## Обновление и удаление

```bash
brigade plugin update cad
brigade plugin remove cad
```

`update` повторно загружает URL, использованный при установке. Удалить плагин нельзя,
пока существуют использующие его сессии. Каталог `plugins_dir` должен входить в backup
вместе с SQLite: база хранит manifest и закреплённые версии, каталог — исполняемые bundles.

Создание собственного плагина описано в [контракте](../reference/plugin-contract.md).
