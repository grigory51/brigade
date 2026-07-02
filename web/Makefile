# Makefile веб-клиента brigade (Vite + React + TS).

.PHONY: install dev build lint clean

# Установить зависимости.
install:
	npm install

# Запустить dev-сервер Vite.
dev:
	npm run dev

# Собрать продакшен-бандл (Vite → web/dist; встраивается в бинарь бэкенда).
# Зависимости ставятся идемпотентно: на чистом checkout (CI, свежий клон) без них
# tsc не видит модулей. npm ci при наличии package-lock.json — детерминированно и
# быстро; иначе fallback на npm install.
build:
	@if [ -f package-lock.json ]; then npm ci; else npm install; fi
	npm run build

# Проверка типов (tsc без эмита) — линт текущего сетапа.
lint:
	npx tsc -b --noEmit

clean:
	rm -rf dist node_modules/.vite tsconfig.tsbuildinfo
