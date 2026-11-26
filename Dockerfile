# Образ сервера brigade: single-binary со встроенным web UI.
#
#   docker build -t brigade .
#
# Сборка повторяет корневой Makefile: web (vite) → go:embed → статический go-бинарь.
# Docker-режим сессий требует проброса /var/run/docker.sock и монтирования workspace
# по ОДИНАКОВОМУ пути внутри и снаружи контейнера (bind-mount'ы сессий выполняет
# докер-демон хоста). См. README → Quick start.

ARG PLUGIN_SOURCE=source

# CI supplies the same MCPB artifacts that are attached to the release.
FROM scratch AS plugins-prebuilt
COPY packaging/bundles/ /bundles/

FROM node:22-bookworm AS plugins-source
ARG VERSION=dev
RUN apt-get update && apt-get install -y --no-install-recommends python3 python3-pip zip \
    && rm -rf /var/lib/apt/lists/*
COPY --from=ghcr.io/astral-sh/uv:0.11.32 /uv /usr/local/bin/uv
WORKDIR /src
COPY plugins/ plugins/
COPY backend/go.mod backend/go.sum backend/
COPY backend/cmd/brigade-coder/ backend/cmd/brigade-coder/
COPY --from=golang:1.26 /usr/local/go /usr/local/go
ENV PATH="/usr/local/go/bin:${PATH}"
RUN plugin_version="${VERSION}" && if [ "$plugin_version" = dev ]; then plugin_version=0.0.0-dev; fi \
    && VERSION="$plugin_version" bash plugins/cad/build.sh \
    && VERSION="$plugin_version" bash plugins/coder/build.sh \
    && mkdir /bundles \
    && cp plugins/cad/brigade-cad.mcpb /bundles/cad.mcpb \
    && cp plugins/coder/brigade-coder.mcpb /bundles/coder.mcpb

FROM plugins-${PLUGIN_SOURCE} AS bundled-plugins

FROM node:22-slim AS web
WORKDIR /src/web
# Версия для показа в UI: git в образе недоступен, поэтому её передаёт build-arg VERSION
# (CI берёт из тега; см. .github/workflows/docker.yml). Локальный docker build без arg → "dev".
ARG VERSION=dev
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN VITE_APP_VERSION=$VERSION npm run build

FROM golang:1.26 AS build
ARG VERSION=dev
WORKDIR /src
COPY backend/go.mod backend/go.sum ./backend/
RUN cd backend && go mod download
COPY backend/ ./backend/
COPY --from=web /src/web/dist/ ./backend/internal/web/dist/
RUN touch backend/internal/web/dist/.gitkeep \
    && cd backend && CGO_ENABLED=0 go build -trimpath -ldflags "-X main.buildVersion=$VERSION" -o /out/brigade ./cmd/brigade

FROM alpine:3.21
LABEL org.opencontainers.image.description="Brigade server and web UI for running coding agents in local or Docker sessions" \
      org.opencontainers.image.documentation="https://github.com/grigory51/brigade/releases"
# git + openssh-client нужны фиче «личная память»: brigade шелл-аутит в git для
# clone/commit/push рабочей копии заметок ПРЯМО В ЭТОМ серверном контейнере, а SSH-remote
# (git@host:...) требует ssh. /root/.ssh — чтобы ssh (accept-new) мог сохранять known_hosts.
RUN apk add --no-cache ca-certificates git openssh-client \
    && mkdir -p /data/workspace /root/.ssh \
    && chmod 700 /root/.ssh
COPY --from=build /out/brigade /usr/local/bin/brigade
COPY --from=bundled-plugins /bundles/ /usr/share/brigade/plugins/
COPY packaging/docker/config.container.yaml /etc/brigade/config.yaml
ENV BRIGADE_CONFIG=/etc/brigade/config.yaml

# /data — состояние (SQLite) и дефолтный workspace; jwt.secret и токен Claude
# задаются через env (BRIGADE_JWT__SECRET, BRIGADE_CLAUDE_CODE_OAUTH_TOKEN).
VOLUME /data
EXPOSE 8080
ENTRYPOINT ["brigade"]
CMD ["--config", "/etc/brigade/config.yaml"]
