# Образ сервера brigade: single-binary со встроенным web UI.
#
#   docker build -t brigade .
#
# Сборка повторяет корневой Makefile: web (vite) → go:embed → статический go-бинарь.
# Docker-режим сессий требует проброса /var/run/docker.sock и монтирования workspace
# по ОДИНАКОВОМУ пути внутри и снаружи контейнера (bind-mount'ы сессий выполняет
# докер-демон хоста). См. README → Quick start.

FROM node:22-slim AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

FROM golang:1.26 AS build
WORKDIR /src
COPY backend/go.mod backend/go.sum ./backend/
RUN cd backend && go mod download
COPY backend/ ./backend/
COPY --from=web /src/web/dist/ ./backend/internal/web/dist/
RUN touch backend/internal/web/dist/.gitkeep \
    && cd backend && CGO_ENABLED=0 go build -trimpath -o /out/brigade ./cmd/brigade

FROM alpine:3.21
RUN apk add --no-cache ca-certificates \
    && mkdir -p /data/workspace
COPY --from=build /out/brigade /usr/local/bin/brigade
COPY docker/config.container.yaml /etc/brigade/config.yaml

# /data — состояние (SQLite) и дефолтный workspace; jwt.secret и токен Claude
# задаются через env (BRIGADE_JWT__SECRET, BRIGADE_CLAUDE_CODE_OAUTH_TOKEN).
VOLUME /data
EXPOSE 8080
ENTRYPOINT ["brigade"]
CMD ["-config", "/etc/brigade/config.yaml"]
