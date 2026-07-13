// Package auth реализует авторизацию brigade: проверку паролей (bcrypt),
// выпуск и проверку access-JWT с коротким TTL, refresh-токены с персистентным
// хранением, HTTP-middleware и Connect-интерсептор (Bearer для mobile, httpOnly-cookie
// для web), а также короткоживущие одноразовые WS-тикеты для апгрейда WebSocket
// (браузер не умеет слать кастомные заголовки при WS-handshake).
package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/grigory51/brigade/backend/internal/secret"
)

// Ошибки уровня сервиса. Возвращаются доменными методами и транслируются
// вызывающим кодом (Connect-хендлером) в соответствующие коды ответа.
var (
	// ErrInvalidCredentials — неверный логин или пароль. Намеренно не различает
	// «нет пользователя» и «неверный пароль», чтобы не давать оракул перебора.
	ErrInvalidCredentials = errors.New("auth: invalid credentials")
	// ErrInvalidToken — токен (access или refresh) недействителен, истёк или отозван.
	ErrInvalidToken = errors.New("auth: invalid token")
)

// User — доменное представление пользователя (без хэша пароля).
type User struct {
	ID       string
	Username string
}

// TokenPair — выпущенная пара токенов и владелец.
type TokenPair struct {
	AccessToken  string
	RefreshToken string
	User         User
	// AccessExpiresAt — момент истечения access-токена; используется web-слоем
	// для выставления Max-Age на cookie.
	AccessExpiresAt time.Time
	// RefreshExpiresAt — момент истечения refresh-токена; используется web-слоем для
	// выставления Max-Age на refresh-cookie.
	RefreshExpiresAt time.Time
}

// Service инкапсулирует операции авторизации поверх store и JWT-issuer'а.
type Service struct {
	db         *sql.DB
	jwt        *JWT
	cipher     *secret.Cipher
	refreshTTL time.Duration
	now        func() time.Time
}

// NewService собирает сервис авторизации.
//
// db — пул соединений store; secret — ключ подписи JWT (и производный ключ шифрования
// секретных настроек); accessTTL/refreshTTL — сроки жизни access- и refresh-токенов.
func NewService(db *sql.DB, serverSecret string, accessTTL, refreshTTL time.Duration) *Service {
	return &Service{
		db:         db,
		jwt:        NewJWT(serverSecret, accessTTL),
		cipher:     secret.NewCipher(serverSecret),
		refreshTTL: refreshTTL,
		now:        time.Now,
	}
}

// JWT возвращает issuer/verifier access-токенов — нужен middleware и интерсептору,
// которые проверяют access-токен без обращения к БД.
func (s *Service) JWT() *JWT { return s.jwt }

// EnsureSeedUser создаёт стартового пользователя, если таблица users пуста.
// Идемпотентно: при наличии хотя бы одного пользователя ничего не делает.
// Пустые username/password трактуются как «сидинг отключён».
func (s *Service) EnsureSeedUser(ctx context.Context, username, password string) error {
	if username == "" || password == "" {
		return nil
	}

	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&count); err != nil {
		return fmt.Errorf("auth: count users: %w", err)
	}
	if count > 0 {
		return nil
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("auth: hash seed password: %w", err)
	}

	id := newID()
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO users (id, username, password_hash, created_at) VALUES (?, ?, ?, ?)`,
		id, username, string(hash), s.now().Unix())
	if err != nil {
		return fmt.Errorf("auth: insert seed user: %w", err)
	}
	return nil
}

// Login проверяет учётные данные и при успехе выпускает пару токенов.
// При неверных данных возвращает ErrInvalidCredentials.
func (s *Service) Login(ctx context.Context, username, password string) (TokenPair, error) {
	id, hash, err := s.userByUsername(ctx, username)
	if errors.Is(err, sql.ErrNoRows) {
		// Сравниваем с фиктивным хэшем, чтобы выровнять время ответа и не давать
		// тайминговый оракул на существование пользователя.
		_ = bcrypt.CompareHashAndPassword(dummyHash, []byte(password))
		return TokenPair{}, ErrInvalidCredentials
	}
	if err != nil {
		return TokenPair{}, err
	}

	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)); err != nil {
		return TokenPair{}, ErrInvalidCredentials
	}

	return s.issuePair(ctx, User{ID: id, Username: username})
}

// IssueForUser выпускает пару токенов для пользователя по имени БЕЗ проверки пароля.
// Используется десктоп-режимом (локальный однопользовательский запуск) для авто-логина
// сид-пользователя без экрана входа. Возвращает ErrInvalidCredentials, если пользователя нет.
func (s *Service) IssueForUser(ctx context.Context, username string) (TokenPair, error) {
	id, _, err := s.userByUsername(ctx, username)
	if errors.Is(err, sql.ErrNoRows) {
		return TokenPair{}, ErrInvalidCredentials
	}
	if err != nil {
		return TokenPair{}, err
	}
	return s.issuePair(ctx, User{ID: id, Username: username})
}

// Refresh обменивает действительный refresh-токен на новую пару токенов.
// Использованный refresh-токен ротируется (удаляется), что ограничивает окно
// повторного применения. Недействительный/истёкший токен — ErrInvalidToken.
func (s *Service) Refresh(ctx context.Context, refreshToken string) (TokenPair, error) {
	userID, err := s.consumeRefreshToken(ctx, refreshToken)
	if err != nil {
		return TokenPair{}, err
	}

	username, err := s.usernameByID(ctx, userID)
	if err != nil {
		return TokenPair{}, err
	}

	return s.issuePair(ctx, User{ID: userID, Username: username})
}

// Logout отзывает переданный refresh-токен (best-effort: отсутствие токена не ошибка).
func (s *Service) Logout(ctx context.Context, refreshToken string) error {
	return s.deleteRefreshToken(ctx, refreshToken)
}

// LogoutAll отзывает все refresh-токены пользователя (выход со всех устройств).
// Используется, когда вызывающий аутентифицирован по access-токену и refresh-токен в
// запросе не передаётся (Logout-эндпоинт принимает пустое тело).
func (s *Service) LogoutAll(ctx context.Context, userID string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE refresh_tokens SET revoked = 1 WHERE user_id = ?`, userID)
	if err != nil {
		return fmt.Errorf("auth: revoke user refresh tokens: %w", err)
	}
	return nil
}

// Me возвращает пользователя по идентификатору (из проверенного access-токена).
func (s *Service) Me(ctx context.Context, userID string) (User, error) {
	username, err := s.usernameByID(ctx, userID)
	if err != nil {
		return User{}, err
	}
	return User{ID: userID, Username: username}, nil
}

// ClaudeTokenSet сообщает, задан ли у пользователя подписочный токен Claude. Само
// значение наружу не отдаётся.
func (s *Service) ClaudeTokenSet(ctx context.Context, userID string) (bool, error) {
	var token string
	err := s.db.QueryRowContext(ctx,
		`SELECT claude_token FROM user_settings WHERE user_id = ?`, userID).Scan(&token)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("auth: query claude token: %w", err)
	}
	return token != "", nil
}

// SetClaudeToken задаёт (или очищает пустым значением) подписочный токен Claude
// пользователя. Значение шифруется перед записью.
func (s *Service) SetClaudeToken(ctx context.Context, userID, token string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO user_settings (user_id, claude_token, updated_at) VALUES (?, ?, ?)
		 ON CONFLICT(user_id) DO UPDATE SET claude_token = excluded.claude_token, updated_at = excluded.updated_at`,
		userID, s.cipher.Encrypt(token), s.now().Unix())
	if err != nil {
		return fmt.Errorf("auth: set claude token: %w", err)
	}
	return nil
}

// MemorySettings возвращает git-remote личной памяти пользователя (его собственный
// репозиторий заметок). Доступ к git@-remote идёт по SSH-ключу агента, отдельного ключа
// памяти нет.
func (s *Service) MemorySettings(ctx context.Context, userID string) (remote string, err error) {
	var encRemote string
	e := s.db.QueryRowContext(ctx,
		`SELECT memory_remote FROM user_settings WHERE user_id = ?`, userID).
		Scan(&encRemote)
	if errors.Is(e, sql.ErrNoRows) {
		return "", nil
	}
	if e != nil {
		return "", fmt.Errorf("auth: query memory settings: %w", e)
	}
	return s.cipher.Decrypt(encRemote), nil
}

// SetMemorySettings задаёт git-remote личной памяти (пустой — отключает память у пользователя).
// remote шифруется перед записью (может нести токен в URL).
func (s *Service) SetMemorySettings(ctx context.Context, userID, remote string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO user_settings (user_id, memory_remote, updated_at) VALUES (?, ?, ?)
		 ON CONFLICT(user_id) DO UPDATE SET
		   memory_remote = excluded.memory_remote,
		   updated_at = excluded.updated_at`,
		userID, s.cipher.Encrypt(remote), s.now().Unix())
	if err != nil {
		return fmt.Errorf("auth: set memory settings: %w", err)
	}
	return nil
}

// NtfySettings возвращает состояние настроек push-уведомлений пользователя: server, topic,
// CSV включённых событий (для показа/редактирования в UI) и флаг «токен задан». Само
// значение токена наружу не отдаётся.
func (s *Service) NtfySettings(ctx context.Context, userID string) (server, topic, events string, tokenSet bool, err error) {
	var encToken string
	e := s.db.QueryRowContext(ctx,
		`SELECT ntfy_server, ntfy_topic, ntfy_token, ntfy_events FROM user_settings WHERE user_id = ?`, userID).
		Scan(&server, &topic, &encToken, &events)
	if errors.Is(e, sql.ErrNoRows) {
		return "", "", "", false, nil
	}
	if e != nil {
		return "", "", "", false, fmt.Errorf("auth: query ntfy settings: %w", e)
	}
	return server, topic, events, encToken != "", nil
}

// SetNtfySettings задаёт server/topic/events и (опционально) токен ntfy. server/topic/events
// перезаписываются всегда (пустой topic отключает уведомления); пустой token СОХРАНЯЕТ
// прежний (чтобы правка server/topic не стирала токен). Токен шифруется перед записью.
func (s *Service) SetNtfySettings(ctx context.Context, userID, server, topic, token, events string) error {
	// CASE сохраняет существующий токен, когда новый пуст (excluded.ntfy_token = '').
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO user_settings (user_id, ntfy_server, ntfy_topic, ntfy_token, ntfy_events, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT(user_id) DO UPDATE SET
		   ntfy_server = excluded.ntfy_server,
		   ntfy_topic = excluded.ntfy_topic,
		   ntfy_token = CASE WHEN excluded.ntfy_token = '' THEN user_settings.ntfy_token ELSE excluded.ntfy_token END,
		   ntfy_events = excluded.ntfy_events,
		   updated_at = excluded.updated_at`,
		userID, server, topic, s.cipher.Encrypt(token), events, s.now().Unix())
	if err != nil {
		return fmt.Errorf("auth: set ntfy settings: %w", err)
	}
	return nil
}

// issuePair выпускает access-JWT и сохраняет новый refresh-токен в store.
func (s *Service) issuePair(ctx context.Context, u User) (TokenPair, error) {
	access, accessExp, err := s.jwt.Issue(u.ID, u.Username, s.now())
	if err != nil {
		return TokenPair{}, err
	}

	refresh, err := s.storeRefreshToken(ctx, u.ID)
	if err != nil {
		return TokenPair{}, err
	}

	return TokenPair{
		AccessToken:      access,
		RefreshToken:     refresh,
		User:             u,
		AccessExpiresAt:  accessExp,
		RefreshExpiresAt: s.now().Add(s.refreshTTL),
	}, nil
}

// dummyHash — bcrypt-хэш произвольной строки, нужный только для выравнивания
// времени ответа при отсутствии пользователя. Сгенерирован один раз на старте.
var dummyHash = mustDummyHash()

func mustDummyHash() []byte {
	h, err := bcrypt.GenerateFromPassword([]byte("brigade-dummy-password"), bcrypt.DefaultCost)
	if err != nil {
		panic(err)
	}
	return h
}
