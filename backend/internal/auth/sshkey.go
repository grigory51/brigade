package auth

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/ssh"
)

// EnsureAgentSSHKey возвращает per-user SSH-ключ агента, генерируя пару при первом обращении.
// Приватный ключ (OpenSSH PEM) и публичный (authorized_keys line) стабильны per-user: приватный
// подкладывается в контейнер сессии (~/.ssh/id_ed25519), публичный пользователь добавляет в
// GitHub. Приватный хранится в БД зашифрованным; наружу (в API) отдаётся только публичный.
func (s *Service) EnsureAgentSSHKey(ctx context.Context, userID string) (privatePEM, publicKey string, err error) {
	var encPriv, pub string
	e := s.db.QueryRowContext(ctx,
		`SELECT agent_ssh_key, agent_ssh_pub FROM user_settings WHERE user_id = ?`, userID).
		Scan(&encPriv, &pub)
	if e != nil && !errors.Is(e, sql.ErrNoRows) {
		return "", "", fmt.Errorf("auth: query ssh key: %w", e)
	}
	if pub != "" {
		return s.cipher.Decrypt(encPriv), pub, nil
	}
	return s.generateAndStoreAgentSSHKey(ctx, userID)
}

// RegenerateAgentSSHKey перевыпускает пару ключей агента (перезаписывает прежнюю). Возвращает
// новый публичный ключ. Прежний публичный ключ в GitHub после этого недействителен.
func (s *Service) RegenerateAgentSSHKey(ctx context.Context, userID string) (publicKey string, err error) {
	_, pub, err := s.generateAndStoreAgentSSHKey(ctx, userID)
	return pub, err
}

// generateAndStoreAgentSSHKey генерирует ed25519-пару, персистит приватный (зашифрован) и
// публичный ключи в user_settings и возвращает их. Комментарий ключа — логин пользователя
// (для опознания в списке ключей GitHub).
func (s *Service) generateAndStoreAgentSSHKey(ctx context.Context, userID string) (privatePEM, publicKey string, err error) {
	comment := "brigade"
	if name, e := s.usernameByID(ctx, userID); e == nil && name != "" {
		comment = "brigade-" + name
	}
	privatePEM, publicKey, err = generateAgentSSHKey(comment)
	if err != nil {
		return "", "", err
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO user_settings (user_id, agent_ssh_key, agent_ssh_pub, updated_at) VALUES (?, ?, ?, ?)
		 ON CONFLICT(user_id) DO UPDATE SET
		   agent_ssh_key = excluded.agent_ssh_key,
		   agent_ssh_pub = excluded.agent_ssh_pub,
		   updated_at = excluded.updated_at`,
		userID, s.cipher.Encrypt(privatePEM), publicKey, s.now().Unix())
	if err != nil {
		return "", "", fmt.Errorf("auth: store ssh key: %w", err)
	}
	return privatePEM, publicKey, nil
}

// generateAgentSSHKey генерирует ed25519-ключ и возвращает приватный в OpenSSH PEM и публичный
// в формате authorized_keys (с комментарием).
func generateAgentSSHKey(comment string) (privatePEM, publicKey string, err error) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", "", fmt.Errorf("auth: generate ed25519: %w", err)
	}
	block, err := ssh.MarshalPrivateKey(priv, comment)
	if err != nil {
		return "", "", fmt.Errorf("auth: marshal private key: %w", err)
	}
	sshPub, err := ssh.NewPublicKey(priv.Public())
	if err != nil {
		return "", "", fmt.Errorf("auth: ssh public key: %w", err)
	}
	pub := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPub))) + " " + comment
	return string(pem.EncodeToMemory(block)), pub, nil
}
