# Makefile бэкенда brigade. Отвечает за единый бинарь: кодген контракта, сборку
# фронтенда (делегируется в web/), его укладку в каталог go:embed и go build.
#
# Примечание про окружение: если `go build` падает с ошибкой вида
# "compile: version does not match go tool version" — в шелл-профиле экспортирована
# переменная GOROOT, указывающая на другую версию Go. Уберите её export из профиля
# (GOROOT определяется самим go-бинарём автоматически); правки в этот Makefile не нужны.

BIN       := bin/brigade
WEB_DIR   := ../web
PROTO_DIR := ../proto
# Собранный фронтенд встраивается в бинарь через go:embed из этого каталога.
EMBED_DIR := internal/web/dist

.PHONY: deps gen build-web embed build build-go run test vet tidy clean

# Скачать зависимости модуля.
deps:
	go mod download

# Кодген Go+TS из proto-контракта (источник истины — ../proto). Делегируется в proto/.
gen:
	$(MAKE) -C $(PROTO_DIR) gen

# Собрать фронтенд (делегируется в web/: Vite → web/dist).
build-web:
	$(MAKE) -C $(WEB_DIR) build

# Уложить собранный фронтенд в каталог go:embed. Без этого шага бинарь встроил бы
# устаревшую сборку фронта, и при запуске отдавался бы неактуальный UI.
embed:
	rm -rf $(EMBED_DIR)
	mkdir -p $(EMBED_DIR)
	cp -R $(WEB_DIR)/dist/. $(EMBED_DIR)/

# Собрать Go-бинарь (фронтенд уже должен быть уложен в EMBED_DIR). Статический бинарь
# без CGO и с -trimpath — для воспроизводимой сборки и встраивания в минимальный образ.
build-go:
	CGO_ENABLED=0 go build -trimpath -o $(BIN) ./cmd/brigade

# Полная сборка единого бинаря: актуальный фронтенд → embed → бинарь. Так `make run`,
# зависящий от build, всегда запускает свежий код фронта и бэкенда.
build: build-web embed build-go

# Запустить собранный бинарь сервиса. Зависит от build: сначала пересобираются все
# запчасти (фронт + бинарь), затем запуск — отдельный шаг сборки перед запуском не нужен.
run: build
	$(BIN) -config config.yaml

# Прогнать тесты.
test:
	go test ./...

# Статический анализ.
vet:
	go vet ./...

# Привести зависимости модуля в порядок.
tidy:
	go mod tidy

clean:
	rm -rf bin $(EMBED_DIR)
