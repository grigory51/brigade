# Makefile brigade — корневой оркестратор. Сами цели реализованы в Makefile подпапок
# (proto/, web/, backend/, mobile/); корень лишь делегирует в них, не дублируя логику.

ROOT        := $(CURDIR)
PROTO_DIR   := $(ROOT)/proto
WEB_DIR     := $(ROOT)/web
BACKEND_DIR := $(ROOT)/backend
MOBILE_DIR  := $(ROOT)/mobile

.PHONY: all proto build-web build build-all build-mobile run test vet clean tidy

all: build

# Полная сборка проекта: единый бинарь (фронт встроен) плюс каркас мобилки.
build-all: build build-mobile

# Единый бинарь со встроенным фронтендом. backend/Makefile сам собирает фронт
# (делегируя в web/), кодген контракта и укладку в go:embed.
build:
	$(MAKE) -C $(BACKEND_DIR) build

# Запуск ранее собранного бинаря. backend/Makefile run зависит от build, поэтому
# пересобирает запчасти перед стартом.
run:
	$(MAKE) -C $(BACKEND_DIR) run

# Кодген Go+TS из proto через buf.
proto:
	$(MAKE) -C $(PROTO_DIR) gen

# Сборка фронтенда (Vite → web/dist).
build-web:
	$(MAKE) -C $(WEB_DIR) build

# Каркас мобильного KMP-проекта.
build-mobile:
	$(MAKE) -C $(MOBILE_DIR) assemble

# Тесты и статический анализ бэкенда.
test:
	$(MAKE) -C $(BACKEND_DIR) test

vet:
	$(MAKE) -C $(BACKEND_DIR) vet

# Привести зависимости бэкенда в порядок.
tidy:
	$(MAKE) -C $(BACKEND_DIR) tidy

# Очистка артефактов всех подпроектов.
clean:
	$(MAKE) -C $(BACKEND_DIR) clean
	$(MAKE) -C $(WEB_DIR) clean
