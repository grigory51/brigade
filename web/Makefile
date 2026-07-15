# Makefile веб-клиента brigade (Vite + React + TS).

.PHONY: install dev build lint clean

# Установить зависимости.
install:
	npm install

# Запустить dev-сервер Vite.
dev:
	npm run dev

# Версия сборки для показа в UI = git-тег (import.meta.env.VITE_APP_VERSION). Вне git/тега →
# "dev"; `?=` позволяет переопределить извне. В docker-образе git недоступен — там версию
# передаёт Dockerfile build-arg'ом (см. web-стадию Dockerfile).
VITE_APP_VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

# Собрать продакшен-бандл (Vite → web/dist; встраивается в бинарь бэкенда).
# Зависимости ставятся идемпотентно: на чистом checkout (CI, свежий клон) без них
# tsc не видит модулей. npm ci при наличии package-lock.json — детерминированно и
# быстро; иначе fallback на npm install.
build:
	@if [ -f package-lock.json ]; then npm ci; else npm install; fi
	VITE_APP_VERSION=$(VITE_APP_VERSION) npm run build

# Проверка типов (tsc без эмита) — линт текущего сетапа.
lint:
	npx tsc -b --noEmit

clean:
	rm -rf dist node_modules/.vite tsconfig.tsbuildinfo
