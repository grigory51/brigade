package auth

import (
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

// TestGenerateAgentSSHKey: приватный ключ парсится как OpenSSH-ключ, публичный — валидная
// authorized_keys-строка, и публичный из приватного совпадает с возвращённым публичным.
func TestGenerateAgentSSHKey(t *testing.T) {
	priv, pub, err := generateAgentSSHKey("brigade-tester")
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	signer, err := ssh.ParsePrivateKey([]byte(priv))
	if err != nil {
		t.Fatalf("приватный ключ не парсится: %v", err)
	}
	if !strings.HasPrefix(pub, "ssh-ed25519 ") {
		t.Errorf("публичный ключ не ed25519: %q", pub)
	}
	if !strings.HasSuffix(pub, " brigade-tester") {
		t.Errorf("публичный ключ без комментария: %q", pub)
	}

	// Публичный ключ, выведенный из приватного, должен совпадать с возвращённым (без комментария).
	derived := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey())))
	if got := strings.TrimSuffix(pub, " brigade-tester"); got != derived {
		t.Errorf("публичный ключ не соответствует приватному:\n got=%q\n want=%q", got, derived)
	}
}
