# Linux-бинарь

Готовый архив содержит Linux amd64 бинарь со встроенным веб-интерфейсом.

```bash
curl -LO https://raw.githubusercontent.com/grigory51/brigade/main/backend/config.example.yaml
mv config.example.yaml config.yaml
curl -L https://github.com/grigory51/brigade/releases/latest/download/brigade-linux-amd64.tar.gz | tar xz
```

Откройте `config.yaml` и обязательно измените:

- `jwt.secret`;
- `seed.username` и `seed.password`;
- абсолютные пути к данным для постоянной установки.

Запуск:

```bash
./brigade --config config.yaml
```

По умолчанию используется `mode: local`: агентские CLI должны быть установлены на хосте и доступны в `PATH`. Для `mode: docker` установите Docker, укажите опубликованный `agent_image` и используйте абсолютные `work_dir` и `agent_home_dir`.
