package auth

import (
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Claims — полезная нагрузка access-токена. Хранит идентификатор и имя пользователя,
// чтобы middleware восстанавливал User без обращения к БД на каждый запрос.
type Claims struct {
	jwt.RegisteredClaims
	Username string `json:"username"`
}

// JWT выпускает и проверяет access-токены, подписанные HMAC-SHA256.
type JWT struct {
	secret    []byte
	accessTTL time.Duration
}

// NewJWT создаёт issuer/verifier с заданным секретом и TTL access-токена.
func NewJWT(secret string, accessTTL time.Duration) *JWT {
	return &JWT{secret: []byte(secret), accessTTL: accessTTL}
}

// Issue выпускает access-токен для пользователя. Возвращает строку токена и момент
// его истечения (для Max-Age cookie). now передаётся явно ради тестируемости.
func (j *JWT) Issue(userID, username string, now time.Time) (string, time.Time, error) {
	exp := now.Add(j.accessTTL)
	claims := Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   userID,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(exp),
		},
		Username: username,
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString(j.secret)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("auth: sign jwt: %w", err)
	}
	return signed, exp, nil
}

// Verify проверяет подпись и срок действия токена и возвращает его claims.
// Любая ошибка валидации (плохая подпись, истёкший срок, неверный алгоритм)
// сворачивается в ErrInvalidToken, чтобы не раскрывать детали клиенту.
func (j *JWT) Verify(tokenStr string) (*Claims, error) {
	claims := &Claims{}
	_, err := jwt.ParseWithClaims(tokenStr, claims, func(t *jwt.Token) (any, error) {
		// Жёстко фиксируем алгоритм: защита от подмены alg (в т.ч. "none").
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return j.secret, nil
	}, jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}))
	if err != nil {
		return nil, errors.Join(ErrInvalidToken, err)
	}
	if claims.Subject == "" {
		return nil, ErrInvalidToken
	}
	return claims, nil
}
