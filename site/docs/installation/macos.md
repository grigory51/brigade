# macOS

Brigade.app — однопользовательское desktop-приложение с локальным сервером и автоматическим входом. Оно поддерживает два режима в **Настройки → Среда агента**:

- **Local** — агенты запускаются процессами на Mac;
- **Docker** — сессии запускаются через выбранный Docker context.

Приложение также может подключаться к удалённым инстансам Brigade, пробрасывать порты сессий на `127.0.0.1` и монтировать их workspace через FUSE-T. См. [Удалённые окружения в Brigade.app](../guides/remote-environments.md).

Скачайте `Brigade-<version>-arm64.dmg` из [последнего релиза](https://github.com/grigory51/brigade/releases/latest), откройте образ и перетащите Brigade в Applications.

Приложение пока не подписано Developer ID. При первом запуске macOS может потребовать открыть его через контекстное меню **Открыть** или разрешить в системных настройках безопасности.

## CAD

Для локальных сессий установите macOS bundle из релиза в конфигурацию Brigade.app:

```bash
BRIGADE_PLUGINS_DIR="$HOME/Library/Application Support/Brigade/plugins" \
  "/Applications/Brigade.app/Contents/MacOS/Brigade" \
  --config "$HOME/Library/Application Support/Brigade/config.yaml" \
  plugin install https://github.com/grigory51/brigade/releases/latest/download/brigade-cad-darwin-arm64.mcpb
```

После этого откройте **Новая сессия** и выберите плитку **CAD**. Для Docker-режима
вместо macOS bundle установите `brigade-cad-linux-amd64.mcpb`.

Сборка из исходников:

```bash
git clone https://github.com/grigory51/brigade.git
cd brigade
make app
open dist/Brigade.app
```

Нужны Xcode Command Line Tools. Сборка включает web UI, Node.js, npm и встроенный MCP Brigade. При первом запуске приложение устанавливает Claude Code, Codex и ACP-адаптеры в `~/Library/Application Support/Brigade/agent-runtime`; при следующих запусках проверяет обновления. Если сеть временно недоступна, уже установленный runtime продолжает работать.
