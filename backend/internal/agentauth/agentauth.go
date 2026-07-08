// Package agentauth — асимметричная авторизация вызовов brigade → агент-демон.
//
// brigade держит приватный Ed25519-ключ и подписывает короткоживущие токены; демон (pid1
// контейнера сессии) получает в env только ПУБЛИЧНЫЙ ключ и проверяет подпись. Это строже
// прежнего симметричного HMAC-токена: утечка env контейнера (публичный ключ) не позволяет
// выдать себя за brigade — подписать может только владелец приватного ключа.
//
// Ключ детерминирован из общего секрета brigade (jwt.secret), поэтому отдельного persist не
// нужно: все демоны получают один публичный ключ, brigade после рестарта подписывает тем же
// приватным. Токен адресован конкретной сессии (aud=sessionID) и короткоживущий (anti-replay).
package agentauth

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// tokenTTL — срок жизни подписанного токена (короткий против replay; вызовы brigade → демон
// быстрые, поэтому запаса хватает).
const tokenTTL = 30 * time.Second

// deriveKey выводит детерминированный Ed25519-ключ из секрета brigade: seed = sha256(context
// + secret). Контекст-префикс изолирует ключ от других производных того же секрета
// (шифрование БД, JWT-подпись пользователей).
func deriveKey(secret string) ed25519.PrivateKey {
	seed := sha256.Sum256([]byte("brigade-agent-ed25519:" + secret))
	return ed25519.NewKeyFromSeed(seed[:])
}

// Signer — сторона brigade: подписывает токены приватным ключом.
type Signer struct{ priv ed25519.PrivateKey }

// NewSigner строит подписанта из секрета brigade (jwt.secret).
func NewSigner(secret string) *Signer { return &Signer{priv: deriveKey(secret)} }

// PublicKeyB64 — публичный ключ в base64 для env контейнера (BRIGADE_DAEMON_PUBKEY). Утечка
// безопасна: им можно только проверять подпись, не подписывать.
func (s *Signer) PublicKeyB64() string {
	pub := s.priv.Public().(ed25519.PublicKey)
	return base64.StdEncoding.EncodeToString(pub)
}

// Token подписывает токен, адресованный сессии sessionID, с коротким сроком жизни.
func (s *Signer) Token(sessionID string) (string, error) {
	now := time.Now()
	claims := jwt.RegisteredClaims{
		Audience:  jwt.ClaimStrings{sessionID},
		IssuedAt:  jwt.NewNumericDate(now),
		ExpiresAt: jwt.NewNumericDate(now.Add(tokenTTL)),
	}
	return jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims).SignedString(s.priv)
}

// Verifier — сторона демона: проверяет подпись публичным ключом. Секрета brigade не имеет.
type Verifier struct{ pub ed25519.PublicKey }

// NewVerifier строит проверяющего из base64-публичного ключа (из env контейнера).
func NewVerifier(pubB64 string) (*Verifier, error) {
	raw, err := base64.StdEncoding.DecodeString(pubB64)
	if err != nil {
		return nil, fmt.Errorf("agentauth: decode public key: %w", err)
	}
	if len(raw) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("agentauth: bad public key size %d", len(raw))
	}
	return &Verifier{pub: ed25519.PublicKey(raw)}, nil
}

// Verify проверяет токен: подпись публичным ключом, метод EdDSA, наличие exp и aud=sessionID
// (токен адресован именно этому демону — отсекает replay токена от другой сессии).
func (v *Verifier) Verify(token, sessionID string) error {
	_, err := jwt.ParseWithClaims(token, &jwt.RegisteredClaims{},
		func(*jwt.Token) (any, error) { return v.pub, nil },
		jwt.WithValidMethods([]string{"EdDSA"}),
		jwt.WithAudience(sessionID),
		jwt.WithExpirationRequired(),
	)
	if err != nil {
		return fmt.Errorf("agentauth: verify: %w", err)
	}
	return nil
}
