// Package store отвечает за работу с SQLite: открытие БД, прогон миграций (goose)
// и доступ к данным. Драйвер modernc.org/sqlite — чистый Go (без cgo), что
// сохраняет самодостаточность бинаря и возможность кросс-компиляции.
package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/pressly/goose/v3"
	"golang.org/x/crypto/bcrypt"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Store — обёртка над пулом соединений к SQLite.
type Store struct {
	db *sql.DB
}

// Open открывает (создаёт при отсутствии) БД по указанному пути и прогоняет миграции.
func Open(path string) (*Store, error) {
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

	return &Store{db: db}, nil
}

// DB возвращает низкоуровневый пул для доменных запросов.
func (s *Store) DB() *sql.DB { return s.db }

// Close закрывает соединение с БД.
func (s *Store) Close() error { return s.db.Close() }

// SeedUser создаёт стартового пользователя с bcrypt-хешем пароля, если таблица
// users пуста. Идемпотентен: при непустой БД ничего не делает. Возвращает true,
// если пользователь был создан. Вызывается на старте бэкенда из конфига.
func (s *Store) SeedUser(ctx context.Context, username, password string) (bool, error) {
	n, err := s.CountUsers(ctx)
	if err != nil {
		return false, err
	}
	if n > 0 {
		return false, nil
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return false, fmt.Errorf("store: seed user: hash password: %w", err)
	}

	err = s.CreateUser(ctx, User{
		ID:           uuid.NewString(),
		Username:     username,
		PasswordHash: string(hash),
		CreatedAt:    time.Now(),
	})
	if err != nil {
		return false, err
	}
	return true, nil
}

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
