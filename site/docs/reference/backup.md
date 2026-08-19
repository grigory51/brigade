# Резервное копирование

Для Docker-установки сохраните весь `BRIGADE_DATA_DIR` (по умолчанию `/srv/brigade`). В нём находятся:

- `brigade.db` — пользователи, сессии, настройки и зашифрованные секреты;
- `brigade.env` — стабильный `jwt.secret`, без которого секреты из БД не расшифровать;
- `workspace/` — рабочие файлы сессий;
- `agent-home/` — runtime-состояние и кеши агентов;
- `memory/` — локальные рабочие копии git-памяти.
- `plugins/` — установленные MCPB bundles и закреплённые версии.

Остановите сервер перед файловой копией SQLite:

```bash
docker stop brigade
tar -czf brigade-backup.tar.gz -C /srv brigade
docker start brigade
```

Репозиторий памяти уже имеет git remote, но он не заменяет backup SQLite и agent home. Храните архив и конфигурацию как секреты.
