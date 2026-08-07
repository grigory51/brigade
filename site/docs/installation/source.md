# Сборка из исходников

Нужны Go, Node.js 22, npm, Git и `make`. Точные версии Go и зависимостей заданы в `backend/go.mod` и lock-файлах.

```bash
git clone https://github.com/grigory51/brigade.git
cd brigade
make build
cp backend/config.example.yaml backend/config.yaml
```

Измените секрет и начальные учётные данные в `backend/config.yaml`, затем запустите:

```bash
make run
```

Полезные цели:

| Команда | Назначение |
| --- | --- |
| `make test` | Go-тесты |
| `make vet` | Статический анализ Go |
| `make build-web` | Сборка React UI |
| `make proto` | Генерация Go и TypeScript из protobuf |
| `make app` | Сборка Brigade.app на macOS |

Для локальной разработки фронтенда выполните `make -C web install`, затем `make -C web dev`.
