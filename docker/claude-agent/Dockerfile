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
# uid ФИКСИРОВАН (1001): brigade создаёт персональный ~/.claude на хосте и chown'ит
# его на этот uid, чтобы bind-mount был writable агентом (иначе root-owned mount →
# EACCES при `/login`). См. spawn.AgentUID.
RUN useradd --create-home --shell /bin/bash --uid 1001 agent \
    && mkdir -p /home/agent/.claude \
    && chown agent:agent /home/agent/.claude
USER agent
WORKDIR /workspace

# Токен подписки передаётся окружением: CLAUDE_CODE_OAUTH_TOKEN.
CMD ["claude"]
