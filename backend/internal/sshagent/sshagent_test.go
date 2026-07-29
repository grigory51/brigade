package sshagent

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"net"
	"path/filepath"
	"testing"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

// Ключ живёт только в памяти процесса, поэтому единственная проверка его пригодности —
// что через сокет отдаётся публичная часть и рабочая подпись.
func TestSSHAgentSignsWithKeyFromMemory(t *testing.T) {
	privPEM, pub := testKey(t)

	a, err := Start(filepath.Join(t.TempDir(), "a.sock"), privPEM)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer a.Close()

	conn, err := net.Dial("unix", a.path)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	client := agent.NewClient(conn)

	keys, err := client.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(keys) != 1 {
		t.Fatalf("ключей в связке: %d, ожидался 1", len(keys))
	}
	if string(keys[0].Marshal()) != string(pub.Marshal()) {
		t.Fatal("агент отдал не тот публичный ключ")
	}

	data := []byte("brigade")
	sig, err := client.Sign(keys[0], data)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if err := pub.Verify(data, sig); err != nil {
		t.Fatalf("подпись не проходит проверку: %v", err)
	}
}

// Перевыпуск ключа пользователя не должен требовать перезапуска хозяина: связка
// заменяется на месте.
func TestSSHAgentReplaceKey(t *testing.T) {
	firstPEM, firstPub := testKey(t)
	secondPEM, secondPub := testKey(t)

	a, err := Start(filepath.Join(t.TempDir(), "a.sock"), firstPEM)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer a.Close()

	if err := a.ReplaceKey(secondPEM); err != nil {
		t.Fatalf("ReplaceKey: %v", err)
	}

	keys, err := a.keyring.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(keys) != 1 {
		t.Fatalf("ключей после замены: %d, ожидался 1", len(keys))
	}
	if string(keys[0].Marshal()) == string(firstPub.Marshal()) {
		t.Fatal("в связке остался прежний ключ")
	}
	if string(keys[0].Marshal()) != string(secondPub.Marshal()) {
		t.Fatal("в связке не новый ключ")
	}
}

// testKey — ed25519-пара в том же формате, что выдаёт auth.EnsureAgentSSHKey.
func testKey(t *testing.T) (privatePEM string, public ssh.PublicKey) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "test")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	pub, err := ssh.NewPublicKey(priv.Public())
	if err != nil {
		t.Fatalf("public: %v", err)
	}
	return string(pem.EncodeToMemory(block)), pub
}
