# macOS

Brigade.app — однопользовательское desktop-приложение с локальным сервером и автоматическим входом. Оно поддерживает два режима в **Настройки → Среда агента**:

- **Local** — агенты запускаются процессами на Mac;
- **Docker** — сессии запускаются через выбранный Docker context.

Сейчас приложение собирается из исходников на Apple Silicon:

```bash
git clone https://github.com/grigory51/brigade.git
cd brigade
make app
open dist/Brigade.app
```

Нужны Xcode Command Line Tools. Сборка включает web UI, Node.js, Claude/Codex ACP-адаптеры и встроенный MCP Brigade. Данные находятся в `~/Library/Application Support/Brigade`.

При первом неподписанном запуске macOS может потребовать разрешить приложение в системных настройках безопасности.
