# Multi-stage сборка единого самодостаточного бинаря brigade.
# Контекст сборки — корень репозитория (нужны и backend/, и web/, и proto/).
#
#   docker build -f backend/Dockerfile -t brigade .

# --- Этап 1: сборка фронтенда (Vite → web/dist) ---
FROM node:22-slim AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json* ./
RUN npm ci
COPY web/ ./
# Сгенерированные из proto TS-типы лежат в web/src/api/gen и копируются вместе с web/.
RUN npm run build

# --- Этап 2: сборка Go-бинаря с вшитым фронтендом ---
FROM golang:1.26 AS build
WORKDIR /src/backend
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend/ ./
# Кладём собранный фронтенд туда, откуда его забирает go:embed (internal/web/dist).
COPY --from=web /src/web/dist ./internal/web/dist
# Чистый Go (modernc.org/sqlite) — cgo не нужен, бинарь статичный.
ENV CGO_ENABLED=0
RUN go build -trimpath -ldflags="-s -w" -o /brigade ./cmd/brigade

# --- Этап 3: минимальный рантайм ---
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /brigade /brigade
EXPOSE 8080
ENTRYPOINT ["/brigade"]
CMD ["-config", "/etc/brigade/config.yaml"]
