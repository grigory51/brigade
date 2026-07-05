# Образ агента brigade: Claude Code CLI + ACP-adapter.
#
#   docker build -t brigade/claude-agent:latest docker/claude-agent
#
# Один образ обслуживает оба режима сессий:
#   - CLI: контейнер запускает `claude` в TTY (docker-режим spawn, attach к терминалу);
#   - ACP: контейнер запускает `claude-agent-acp` со stdio JSON-RPC (без TTY).
# Команда задаётся при создании контейнера, ENTRYPOINT намеренно не фиксируется.
#
# Весь home агента (/home/agent) brigade bind-mount'ит per-user с хоста: состояние
# Claude (~/.claude, ~/.claude.json) и рабочие файлы (~/workspace) переживают
# пересоздание контейнера и общие между контейнерами пользователя. Каталоги в образе
# создаются как fallback для случая, когда home не смонтирован (claude_home_dir пуст).

FROM node:22-slim

# Инструменты, без которых кодинг-агент недееспособен в контейнере. Базовый node:22-slim
# несёт минимум ОС-утилит, поэтому доставляем ходовой набор для «простых операций»:
# less (пейджер git), jq (JSON), python3-pip/venv (python3 без pip бесполезен),
# unzip/zip/wget (архивы/загрузки), procps (ps), openssh-client (git по SSH), patch,
# file, tree, vim-tiny (git $EDITOR). Компиляторы (build-essential) намеренно НЕ ставим —
# образ остаётся лёгким; если понадобится сборка нативных зависимостей, добавить отдельно.
RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        git ripgrep ca-certificates curl wget \
        python3 python3-pip python3-venv \
        jq less unzip zip procps openssh-client patch file tree vim-tiny \
    && rm -rf /var/lib/apt/lists/*

# Adapter не ниже 0.54: в 0.52 session/set_config_option (смена модели) рассинхро-
# нивался с SDK-процессом — adapter рапортовал новую модель, а отвечала старая.
RUN npm install -g @anthropic-ai/claude-code "@agentclientprotocol/claude-agent-acp@^0.54.1" \
    && npm cache clean --force

# brigade MCP-сервер кастомных UI-инструментов (render_ui, show_choice). Сток-адаптер
# игнорирует кастомный _meta, но пробрасывает ACP mcpServers в Claude Agent SDK; SDK
# стартует этот сервер как stdio-subprocess. Кладём в фиксированный путь и ставим
# зависимости рядом со скриптом, чтобы резолв @modelcontextprotocol/sdk не зависел от cwd
# сессии. Каталог root-owned, читаем пользователем agent.
COPY mcp/ /opt/brigade-mcp/
RUN cd /opt/brigade-mcp \
    && npm install --omit=dev \
    && npm cache clean --force

# Непривилегированный пользователь: агент исполняет произвольные команды.
# uid ФИКСИРОВАН (1001): brigade создаёт персональный home на хосте и chown'ит его на
# этот uid, чтобы bind-mount был writable агентом (иначе root-owned mount → EACCES при
# `/login`). См. spawn.AgentUID.
RUN useradd --create-home --shell /bin/bash --uid 1001 agent \
    && mkdir -p /home/agent/.claude /home/agent/workspace \
    && chown -R agent:agent /home/agent
USER agent
# Рабочая директория агента — подпапка home (~/workspace): в docker-режиме brigade
# монтирует home per-user, файлы проекта не расшарены между пользователями.
WORKDIR /home/agent/workspace

# Токен подписки передаётся окружением: CLAUDE_CODE_OAUTH_TOKEN.
CMD ["claude"]
