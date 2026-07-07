// Package store отвечает за работу с SQLite: открытие БД, прогон миграций (goose)
// и доступ к данным. Драйвер modernc.org/sqlite — чистый Go (без cgo), что
// сохраняет самодостаточность бинаря и возможность кросс-компиляции.
package store

import (
	"database/sql"
	"embed"
	"fmt"

	"github.com/pressly/goose/v3"

	_ "modernc.org/sqlite"

	"github.com/grigory51/brigade/backend/internal/secret"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Store — обёртка над пулом соединений к SQLite. cipher шифрует/дешифрует секретные
// колонки user_settings (токен Claude, ключ памяти); может быть nil (тесты) — тогда
// значения проходят как есть.
type Store struct {
	db     *sql.DB
	cipher *secret.Cipher
}

// Open открывает (создаёт при отсутствии) БД по указанному пути и прогоняет миграции.
// cipher используется для прозрачного шифрования секретных полей (nil — без шифрования).
func Open(path string, cipher *secret.Cipher) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("store: open %q: %w", path, err)
	}

	// SQLite не поддерживает параллельную запись несколькими соединениями;
	// один writer исключает блокировки "database is locked".
	db.SetMaxOpenConns(1)

	if err := migrate(db); err != nil {
		_ = db.Close()
		return nil, err
	}

	return &Store{db: db, cipher: cipher}, nil
}

// DB возвращает низкоуровневый пул для доменных запросов.
func (s *Store) DB() *sql.DB { return s.db }

// Close закрывает соединение с БД.
func (s *Store) Close() error { return s.db.Close() }

func migrate(db *sql.DB) error {
	goose.SetBaseFS(migrations)
	if err := goose.SetDialect("sqlite3"); err != nil {
		return fmt.Errorf("store: set dialect: %w", err)
	}
	if err := goose.Up(db, "migrations"); err != nil {
		return fmt.Errorf("store: migrate: %w", err)
	}
	return nil
}
