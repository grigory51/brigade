# Makefile веб-клиента brigade (Vite + React + TS).

.PHONY: install dev build lint clean

# Установить зависимости.
install:
	npm install

# Запустить dev-сервер Vite.
dev:
	npm run dev

# Собрать продакшен-бандл (Vite → web/dist; встраивается в бинарь бэкенда).
build:
	npm run build

# Проверка типов (tsc без эмита) — линт текущего сетапа.
lint:
	npx tsc -b --noEmit

clean:
	rm -rf dist node_modules/.vite tsconfig.tsbuildinfo
