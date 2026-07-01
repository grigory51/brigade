package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
)

// userByUsername возвращает id и хэш пароля по имени пользователя.
// При отсутствии пользователя возвращает sql.ErrNoRows.
func (s *Service) userByUsername(ctx context.Context, username string) (id, hash string, err error) {
	err = s.db.QueryRowContext(ctx,
		`SELECT id, password_hash FROM users WHERE username = ?`, username).
		Scan(&id, &hash)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", "", err
		}
		return "", "", fmt.Errorf("auth: query user: %w", err)
	}
	return id, hash, nil
}

// usernameByID возвращает имя пользователя по идентификатору.
// Отсутствие пользователя трактуется как ErrInvalidToken: субъект токена больше
// не существует, токен следует считать недействительным.
func (s *Service) usernameByID(ctx context.Context, id string) (string, error) {
	var username string
	err := s.db.QueryRowContext(ctx,
		`SELECT username FROM users WHERE id = ?`, id).Scan(&username)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", ErrInvalidToken
		}
		return "", fmt.Errorf("auth: query username: %w", err)
	}
	return username, nil
}

// storeRefreshToken генерирует новый refresh-токен, сохраняет его хэш в БД и
// возвращает сам токен (хранится только хэш — см. миграцию 00002).
func (s *Service) storeRefreshToken(ctx context.Context, userID string) (string, error) {
	token, err := randomToken()
	if err != nil {
		return "", err
	}

	now := s.now()
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO refresh_tokens (id, user_id, token_hash, expires_at, created_at) VALUES (?, ?, ?, ?, ?)`,
		newID(), userID, hashToken(token), now.Add(s.refreshTTL).Unix(), now.Unix())
	if err != nil {
		return "", fmt.Errorf("auth: insert refresh token: %w", err)
	}
	return token, nil
}

// consumeRefreshToken проверяет refresh-токен и атомарно его отзывает (ротация).
// Возвращает id владельца либо ErrInvalidToken, если токен не найден, уже отозван
// или истёк. Отзыв выполняется флагом revoked (строка сохраняется как аудит-след),
// а не физическим удалением.
func (s *Service) consumeRefreshToken(ctx context.Context, token string) (string, error) {
	if token == "" {
		return "", ErrInvalidToken
	}
	hash := hashToken(token)

	var userID string
	var expiresAt int64
	var revoked int
	err := s.db.QueryRowContext(ctx,
		`SELECT user_id, expires_at, revoked FROM refresh_tokens WHERE token_hash = ?`, hash).
		Scan(&userID, &expiresAt, &revoked)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", ErrInvalidToken
		}
		return "", fmt.Errorf("auth: query refresh token: %w", err)
	}

	if revoked != 0 {
		return "", ErrInvalidToken
	}

	// Ротация: токен одноразовый — помечаем отозванным независимо от срока годности.
	if _, err := s.db.ExecContext(ctx,
		`UPDATE refresh_tokens SET revoked = 1 WHERE token_hash = ?`, hash); err != nil {
		return "", fmt.Errorf("auth: revoke refresh token: %w", err)
	}

	if s.now().Unix() >= expiresAt {
		return "", ErrInvalidToken
	}
	return userID, nil
}

// deleteRefreshToken отзывает refresh-токен (logout). Отсутствие токена не ошибка.
func (s *Service) deleteRefreshToken(ctx context.Context, token string) error {
	if token == "" {
		return nil
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE refresh_tokens SET revoked = 1 WHERE token_hash = ?`, hashToken(token))
	if err != nil {
		return fmt.Errorf("auth: revoke refresh token: %w", err)
	}
	return nil
}

// hashToken возвращает hex-представление sha-256 от токена. В БД хранится только
// хэш, что обесценивает refresh-токены при утечке содержимого БД.
func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// randomToken генерирует криптостойкий случайный токен (256 бит, url-safe base64).
func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("auth: read random: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// newID генерирует идентификатор сущности (128 бит, url-safe base64).
func newID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand на поддерживаемых платформах не отказывает; паника здесь
		// сигнализирует о фатально неработоспособном окружении.
		panic(fmt.Sprintf("auth: read random for id: %v", err))
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
