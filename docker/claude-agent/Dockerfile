# Образ агента brigade: Claude Code CLI + ACP-adapter.
#
#   docker build -t brigade/claude-agent:latest docker/claude-agent
#
# Один образ обслуживает оба режима сессий:
#   - CLI: контейнер запускает `claude` в TTY (docker-режим spawn, attach к терминалу);
#   - ACP: контейнер запускает `claude-agent-acp` со stdio JSON-RPC (без TTY).
# Команда задаётся при создании контейнера, ENTRYPOINT намеренно не фиксируется.
#
# Состояние агента (~/.claude: сессии, конфиг) живёт в /home/agent/.claude —
# brigade монтирует туда named volume сессии, чтобы состояние переживало пересоздание
# контейнера (restore = новый контейнер + session/load).

FROM node:22-slim

# Инструменты, без которых кодинг-агент недееспособен в контейнере.
RUN apt-get update \
    && apt-get install -y --no-install-recommends git ripgrep ca-certificates curl python3 \
    && rm -rf /var/lib/apt/lists/*

# Adapter не ниже 0.54: в 0.52 session/set_config_option (смена модели) рассинхро-
# нивался с SDK-процессом — adapter рапортовал новую модель, а отвечала старая.
RUN npm install -g @anthropic-ai/claude-code "@agentclientprotocol/claude-agent-acp@^0.54.1" \
    && npm cache clean --force

# Непривилегированный пользователь: агент исполняет произвольные команды.
# Каталог состояния создаётся заранее с правами агента: пустой named volume наследует
# ownership точки монтирования из образа (иначе mount был бы root-owned и агент падал
# бы с EACCES).
RUN useradd --create-home --shell /bin/bash agent \
    && mkdir -p /home/agent/.claude \
    && chown agent:agent /home/agent/.claude
USER agent
WORKDIR /workspace

# Токен подписки передаётся окружением: CLAUDE_CODE_OAUTH_TOKEN.
CMD ["claude"]
