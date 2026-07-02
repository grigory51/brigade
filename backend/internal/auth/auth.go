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
	refreshTTL time.Duration
	now        func() time.Time
}

// NewService собирает сервис авторизации.
//
// db — пул соединений store; secret — ключ подписи JWT; accessTTL/refreshTTL —
// сроки жизни access- и refresh-токенов соответственно.
func NewService(db *sql.DB, secret string, accessTTL, refreshTTL time.Duration) *Service {
	return &Service{
		db:         db,
		jwt:        NewJWT(secret, accessTTL),
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
// пользователя.
func (s *Service) SetClaudeToken(ctx context.Context, userID, token string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO user_settings (user_id, claude_token, updated_at) VALUES (?, ?, ?)
		 ON CONFLICT(user_id) DO UPDATE SET claude_token = excluded.claude_token, updated_at = excluded.updated_at`,
		userID, token, s.now().Unix())
	if err != nil {
		return fmt.Errorf("auth: set claude token: %w", err)
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
