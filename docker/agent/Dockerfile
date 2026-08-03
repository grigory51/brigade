# Образ агента brigade: runtime-компоненты (демон, node, Claude Code CLI, ACP-адаптер,
# MCP-сервер brigade) плюс базовое окружение для работы агента.
#
#   docker build -t brigade/agent:latest -f docker/agent/Dockerfile .
#
# ВАЖНО: контекст сборки — КОРЕНЬ репо (нужен backend/ для бинаря brigade), поэтому пути
# COPY даны от корня (docker/agent/...). CI собирает образ с context: "." .
#
# Один образ обслуживает оба режима сессий:
#   - CLI: контейнер запускает `claude` в TTY (durable-терминал per-user демона);
#   - ACP: контейнер запускает `brigade acp-agent` — durable-демон (pid1), который сам
#     спавнит `claude-agent-acp` и переживает рестарт brigade (см. internal/acpdaemon).
# Команда задаётся при создании контейнера, ENTRYPOINT намеренно не фиксируется.
#
# Образ ещё и ДОНОР runtime: всё, что brigade привозит в контейнер сессии, лежит в
# /opt/brigade-runtime/<слой>/ и раскладывается по read-only volume'ам (см. internal/spawn
# и манифест internal/agent). Благодаря этому сессия может работать и на ЧУЖОМ образе
# (ubuntu, golang, python) — компоненты приедут снаружи, а не возьмутся из образа.
# Имена каталогов слоёв ДОЛЖНЫ совпадать с манифестом (internal/agent): расхождение
# ловится при подготовке volume'ов.

# Бинарь brigade (тот же, что сервер; режим `acp-agent` — демон). cgo-free (modernc sqlite),
# статический — не зависит от libc образа, в котором будет исполняться. go:embed web/dist
# довольствуется .gitkeep (демону web не нужен).
FROM golang:1.26 AS brigade-build
WORKDIR /src
COPY backend/go.mod backend/go.sum ./backend/
RUN cd backend && go mod download
COPY backend/ ./backend/
# Демону web не нужен, но go:embed all:dist требует непустой каталог; в build-контексте
# dist исключён .dockerignore — кладём заглушку .gitkeep.
RUN mkdir -p backend/internal/web/dist && touch backend/internal/web/dist/.gitkeep \
    && cd backend && CGO_ENABLED=0 go build -trimpath -o /out/brigade ./cmd/brigade

# Node runtime по-прежнему берём из bullseye (glibc 2.31): этот слой исполняется и в
# пользовательских образах, включая ubuntu:jammy (glibc 2.35). Сам образ по умолчанию —
# актуальная Ubuntu с полноценным Python-окружением.
FROM node:22-bullseye-slim AS node-runtime
FROM ghcr.io/astral-sh/uv:0.11.32 AS uv-runtime
FROM ubuntu:26.04

COPY --from=node-runtime /usr/local/bin/node /usr/local/bin/node
COPY --from=node-runtime /usr/local/lib/node_modules /usr/local/lib/node_modules
COPY --from=uv-runtime /uv /uvx /usr/local/bin/
RUN ln -s /usr/local/lib/node_modules/npm/bin/npm-cli.js /usr/local/bin/npm \
    && ln -s /usr/local/lib/node_modules/npm/bin/npx-cli.js /usr/local/bin/npx

# Инструменты, без которых кодинг-агент недееспособен в контейнере. Базовый slim-образ
# несёт минимум ОС-утилит, поэтому доставляем ходовой набор для «простых операций»:
# less (пейджер git), jq (JSON), Python с привычными `python`/`python3`, pip/venv и uv,
# unzip/zip/wget (архивы/загрузки), procps (ps), openssh-client (git по SSH), patch,
# file, tree, vim-tiny (git $EDITOR). Компиляторы (build-essential) намеренно НЕ ставим —
# образ остаётся лёгким; нужен тяжёлый тулчейн — пользователь приносит свой образ.
RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        git ripgrep ca-certificates curl wget libstdc++6 libgcc-s1 \
        python3 python3-pip python3-venv \
        jq less unzip zip procps openssh-client patch file tree vim-tiny \
    && ln -s /usr/bin/python3 /usr/local/bin/python \
    && rm -rf /var/lib/apt/lists/*

# --- Слои runtime -----------------------------------------------------------------

# node — интерпретатор для агентов, написанных на Node (Claude Code и его ACP-адаптер).
# Копия из образа, а не отдельная установка: версия та же, что у npm-пакетов ниже.
RUN mkdir -p /opt/brigade-runtime/node/bin /opt/brigade-runtime/node/lib \
    && cp /usr/local/bin/node /opt/brigade-runtime/node/bin/ \
    && cp -a /usr/local/lib/node_modules /opt/brigade-runtime/node/lib/

# Adapter держим в ногу с claude-code (тот ставится latest): каретка ^0.54.1 капалась на
# 0.54.x, отставая от свежего SDK на несколько миноров — старый adapter не прорелеивал через
# ACP новые механизмы (напр. structured-output инструмента Workflow ломался на фазе Scope).
# Плавающий минор держит adapter актуальным. (Исторически ≥0.54: в 0.52
# session/set_config_option рассинхронивался с SDK — adapter рапортовал новую модель, а
# отвечала старая.)
#
# Эта строка ARG — ЕДИНЫЙ источник версии адаптера: scripts/make-app.sh вычитывает её
# отсюда для агент-рантайма в Brigade.app, чтобы .app и контейнер не разъезжались.
# Формат строки (`ARG ACP_ADAPTER_VERSION=<spec>`) менять только вместе со скриптом.
ARG ACP_ADAPTER_VERSION=^0.62.0
ARG CODEX_ACP_VERSION=^1.1.7

# claude-cli и acp-adapter — отдельными слоями: CLI-сессии адаптер не нужен, ACP-сессии не
# нужен интерактивный claude. --prefix кладёт пакет со своими bin/ и lib/ в каталог слоя;
# шебанг у них `#!/usr/bin/env node`, поэтому node берётся из PATH (слой node).
RUN npm install -g --prefix /opt/brigade-runtime/claude-cli @anthropic-ai/claude-code \
    && npm install -g --prefix /opt/brigade-runtime/acp-adapter "@agentclientprotocol/claude-agent-acp@${ACP_ADAPTER_VERSION}" \
    && npm install -g --prefix /opt/brigade-runtime/codex "@agentclientprotocol/codex-acp@${CODEX_ACP_VERSION}" @openai/codex \
    && npm cache clean --force

# brigade MCP-сервер кастомных UI-инструментов (render_ui, show_choice). Сток-адаптер
# игнорирует кастомный _meta, но пробрасывает ACP mcpServers в Claude Agent SDK; SDK
# стартует этот сервер как stdio-subprocess. Зависимости ставятся рядом со скриптом, чтобы
# резолв @modelcontextprotocol/sdk не зависел от cwd сессии.
COPY docker/agent/mcp/ /opt/brigade-runtime/brigade-mcp/
RUN cd /opt/brigade-runtime/brigade-mcp \
    && npm install --omit=dev \
    && npm cache clean --force

# daemon — бинарь brigade (`brigade acp-agent`, pid1 контейнеров сессий). Ему brigade
# передаёт секреты по RPC, поэтому он ВСЕГДА приезжает слоем, а не берётся из образа
# сессии: подменить его содержимым чужого образа нельзя.
COPY --from=brigade-build /out/brigade /opt/brigade-runtime/daemon/bin/brigade

# --- Образ по умолчанию -----------------------------------------------------------

# Сессии на этом образе получают те же компоненты слоями (как и на любом другом), но
# PATH прописан и здесь: так образ остаётся самодостаточным для ручного запуска и отладки
# (`docker run -it brigade/agent claude`).
ENV PATH=/opt/brigade-runtime/daemon/bin:/opt/brigade-runtime/claude-cli/bin:/opt/brigade-runtime/acp-adapter/bin:/opt/brigade-runtime/codex/bin:$PATH

# Непривилегированный пользователь: агент исполняет произвольные команды.
# uid ФИКСИРОВАН (1001): brigade создаёт персональный home на хосте и chown'ит его на
# этот uid, чтобы bind-mount был writable агентом (иначе root-owned mount → EACCES при
# `/login`). См. spawn.AgentUID. Того же требуем от пользовательских образов.
RUN useradd --create-home --shell /bin/bash --uid 1001 agent \
    && mkdir -p /home/agent/.claude /home/agent/workspace \
    && chown -R agent:agent /home/agent
USER agent
# Рабочая директория агента — подпапка home (~/workspace): в docker-режиме brigade
# монтирует home per-user, файлы проекта не расшарены между пользователями.
WORKDIR /home/agent/workspace

# Токен подписки передаётся окружением: CLAUDE_CODE_OAUTH_TOKEN.
CMD ["claude"]
