package auth

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// TestJWTIssueVerifyRoundtrip проверяет, что выпущенный токен успешно
// верифицируется тем же issuer'ом и восстанавливает исходные claims.
func TestJWTIssueVerifyRoundtrip(t *testing.T) {
	j := NewJWT("test-secret", time.Hour)
	// Verify сверяет exp с реальным time.Now() (jwt-библиотека не принимает clock
	// извне), поэтому выпускаем токен от текущего момента, а не от фиксированного.
	now := time.Now()

	token, exp, err := j.Issue("user-1", "alice", now)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if !exp.Equal(now.Add(time.Hour)) {
		t.Fatalf("exp = %v, want %v", exp, now.Add(time.Hour))
	}

	claims, err := j.Verify(token)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if claims.Subject != "user-1" {
		t.Errorf("Subject = %q, want %q", claims.Subject, "user-1")
	}
	if claims.Username != "alice" {
		t.Errorf("Username = %q, want %q", claims.Username, "alice")
	}
}

// TestJWTVerifyExpired проверяет, что просроченный токен отвергается и ошибка
// сворачивается в ErrInvalidToken.
func TestJWTVerifyExpired(t *testing.T) {
	j := NewJWT("test-secret", time.Minute)
	issuedAt := time.Now().Add(-time.Hour)

	token, _, err := j.Issue("user-1", "alice", issuedAt)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	if _, err := j.Verify(token); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("Verify expired: err = %v, want ErrInvalidToken", err)
	}
}

// TestJWTVerifyWrongSecret проверяет, что токен, подписанный другим секретом,
// не проходит верификацию.
func TestJWTVerifyWrongSecret(t *testing.T) {
	issuer := NewJWT("secret-a", time.Hour)
	verifier := NewJWT("secret-b", time.Hour)

	token, _, err := issuer.Issue("user-1", "alice", time.Now())
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	if _, err := verifier.Verify(token); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("Verify wrong secret: err = %v, want ErrInvalidToken", err)
	}
}

// TestJWTVerifyRejectsNoneAlg проверяет защиту от подмены алгоритма: токен с
// alg=none должен отвергаться.
func TestJWTVerifyRejectsNoneAlg(t *testing.T) {
	j := NewJWT("test-secret", time.Hour)
	// Заголовок {"alg":"none","typ":"JWT"} и тело с непустым subject, без подписи.
	const noneToken = "eyJhbGciOiJub25lIiwidHlwIjoiSldUIn0." +
		"eyJzdWIiOiJ1c2VyLTEifQ."

	if _, err := j.Verify(noneToken); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("Verify none-alg: err = %v, want ErrInvalidToken", err)
	}
}

// TestJWTVerifyMalformed проверяет, что синтаксически некорректный токен
// отвергается без паники.
func TestJWTVerifyMalformed(t *testing.T) {
	j := NewJWT("test-secret", time.Hour)
	for _, tok := range []string{"", "not-a-jwt", strings.Repeat("a.", 3)} {
		if _, err := j.Verify(tok); !errors.Is(err, ErrInvalidToken) {
			t.Errorf("Verify(%q): err = %v, want ErrInvalidToken", tok, err)
		}
	}
}
