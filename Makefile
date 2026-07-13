# Makefile brigade — корневой оркестратор. Сами цели реализованы в Makefile подпапок
# (proto/, web/, backend/, mobile/); корень лишь делегирует в них, не дублируя логику.

ROOT        := $(CURDIR)
PROTO_DIR   := $(ROOT)/proto
WEB_DIR     := $(ROOT)/web
BACKEND_DIR := $(ROOT)/backend
MOBILE_DIR  := $(ROOT)/mobile

.PHONY: all proto build-web build build-all build-mobile run app test vet clean tidy release

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

# Собрать macOS-десктоп Brigade.app (нативное окно webview) → dist/Brigade.app.
# Только macOS (нужен cgo/Xcode CLT для webview).
app:
	$(MAKE) -C $(BACKEND_DIR) app

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

# Релиз: инкремент последнего semver-тега и push — docker-workflow CI собирает и
# публикует образы с этой версией. Источник истины версии — git-тег, в файлах
# проекта она не хранится.
#   make release              # patch: v0.1.0 → v0.1.1
#   make release BUMP=minor   # v0.1.1 → v0.2.0
#   make release BUMP=major   # v0.2.0 → v1.0.0
BUMP ?= patch
release:
	@test -z "$$(git status --porcelain)" || { echo "error: working tree is dirty"; exit 1; }
	@last=$$(git tag --list 'v*' --sort=-v:refname | head -1); \
	last=$${last:-v0.0.0}; \
	ver=$${last#v}; \
	major=$${ver%%.*}; rest=$${ver#*.}; minor=$${rest%%.*}; patch=$${rest#*.}; \
	case "$(BUMP)" in \
	  major) major=$$((major+1)); minor=0; patch=0 ;; \
	  minor) minor=$$((minor+1)); patch=0 ;; \
	  patch) patch=$$((patch+1)) ;; \
	  *) echo "error: BUMP must be major|minor|patch"; exit 1 ;; \
	esac; \
	next="v$$major.$$minor.$$patch"; \
	echo "$$last -> $$next"; \
	git tag -a "$$next" -m "$$next" && git push origin "$$next"
