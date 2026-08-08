// Package auth реализует авторизацию brigade: проверку паролей (bcrypt),
// выпуск и проверку access-JWT с коротким TTL, refresh-токены с персистентным
// хранением, HTTP-middleware и Connect-интерсептор (Bearer для mobile, httpOnly-cookie
// для web), а также короткоживущие одноразовые WS-тикеты для апгрейда WebSocket
// (браузер не умеет слать кастомные заголовки при WS-handshake).
package auth

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
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

// LoginExternal находит или создаёт локального пользователя по стабильной паре
// issuer+subject. Совпадение отображаемого username намеренно не связывает учётные записи:
// это позволило бы внешнему провайдеру захватить существующий password-аккаунт.
func (s *Service) LoginExternal(ctx context.Context, provider, subject, username string) (TokenPair, error) {
	if provider == "" || subject == "" {
		return TokenPair{}, ErrInvalidCredentials
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return TokenPair{}, fmt.Errorf("auth: begin external login: %w", err)
	}
	defer tx.Rollback()

	var user User
	err = tx.QueryRowContext(ctx, `SELECT u.id, u.username FROM auth_identities i JOIN users u ON u.id = i.user_id WHERE i.provider = ? AND i.subject = ?`, provider, subject).Scan(&user.ID, &user.Username)
	if errors.Is(err, sql.ErrNoRows) {
		username = strings.TrimSpace(username)
		if runes := []rune(username); len(runes) > 128 {
			username = string(runes[:128])
		}
		if username == "" {
			username = "oidc-" + hashToken(provider + "\x00" + subject)[:8]
		}
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE username = ?`, username).Scan(&exists); err != nil {
			return TokenPair{}, fmt.Errorf("auth: check external username: %w", err)
		}
		if exists != 0 {
			username += "-" + hashToken(provider + "\x00" + subject)[:8]
		}
		user = User{ID: newID(), Username: username}
		if _, err := tx.ExecContext(ctx, `INSERT INTO users (id, username, password_hash, created_at) VALUES (?, ?, '', ?)`, user.ID, user.Username, s.now().Unix()); err != nil {
			return TokenPair{}, fmt.Errorf("auth: insert external user: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO auth_identities (provider, subject, user_id, created_at) VALUES (?, ?, ?, ?)`, provider, subject, user.ID, s.now().Unix()); err != nil {
			return TokenPair{}, fmt.Errorf("auth: insert identity: %w", err)
		}
	} else if err != nil {
		return TokenPair{}, fmt.Errorf("auth: query identity: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return TokenPair{}, fmt.Errorf("auth: commit external login: %w", err)
	}
	return s.issuePair(ctx, user)
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

// CodexSettings возвращает только состояние профилей; секреты наружу не выдаются.
func (s *Service) CodexSettings(ctx context.Context, userID string) (apiKeySet, chatGPTConnected bool, defaultProfile string, err error) {
	var apiKey, authJSON string
	err = s.db.QueryRowContext(ctx, `SELECT codex_api_key, codex_auth_json, codex_default_profile FROM user_settings WHERE user_id = ?`, userID).
		Scan(&apiKey, &authJSON, &defaultProfile)
	if errors.Is(err, sql.ErrNoRows) {
		return false, false, "", nil
	}
	if err != nil {
		return false, false, "", fmt.Errorf("auth: query codex settings: %w", err)
	}
	if defaultProfile == "" || (defaultProfile == "chatgpt" && authJSON == "") || (defaultProfile == "api-key" && apiKey == "") {
		if authJSON != "" {
			defaultProfile = "chatgpt"
		} else if apiKey != "" {
			defaultProfile = "api-key"
		} else {
			defaultProfile = ""
		}
	}
	return apiKey != "", authJSON != "", defaultProfile, nil
}

func (s *Service) SetCodexAPIKey(ctx context.Context, userID, apiKey string) error {
	return s.setCodexSetting(ctx, userID, "codex_api_key", s.cipher.Encrypt(apiKey))
}

func (s *Service) SetCodexAuthJSON(ctx context.Context, userID, authJSON string) error {
	if authJSON != "" && !json.Valid([]byte(authJSON)) {
		return errors.New("auth: codex auth.json is not valid JSON")
	}
	return s.setCodexSetting(ctx, userID, "codex_auth_json", s.cipher.Encrypt(authJSON))
}

func (s *Service) SetCodexDefaultProfile(ctx context.Context, userID, profile string) error {
	if profile != "" && profile != "api-key" && profile != "chatgpt" {
		return errors.New("auth: unknown codex profile")
	}
	return s.setCodexSetting(ctx, userID, "codex_default_profile", profile)
}

func (s *Service) setCodexSetting(ctx context.Context, userID, column, value string) error {
	query := fmt.Sprintf(`INSERT INTO user_settings (user_id, %s, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(user_id) DO UPDATE SET %s = excluded.%s, updated_at = excluded.updated_at`, column, column, column)
	if _, err := s.db.ExecContext(ctx, query, userID, value, s.now().Unix()); err != nil {
		return fmt.Errorf("auth: set codex setting: %w", err)
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
