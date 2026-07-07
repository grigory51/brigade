// Package secret шифрует секретные поля перед записью в БД ключом, производным от
// серверного секрета (jwt.secret). Назначение — не пускать креды пользователей (токен
// Claude, приватный SSH-ключ памяти) в БД открытым текстом: утечка дампа БД без секрета
// бесполезна. Это НЕ защищает при одновременной утечке БД и конфига (секрет там же).
package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"io"
	"strings"
)

// prefix помечает наш ciphertext, чтобы Decrypt отличал зашифрованные значения от
// legacy-plaintext (записанных до включения шифрования) и читал последние как есть.
const prefix = "enc:v1:"

// Cipher — AES-256-GCM поверх ключа sha256(серверный секрет).
type Cipher struct{ aead cipher.AEAD }

// NewCipher строит Cipher из серверного секрета. Паника невозможна: длина ключа корректна.
func NewCipher(serverSecret string) *Cipher {
	key := sha256.Sum256([]byte("brigade-field-enc:" + serverSecret))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		panic("secret: aes: " + err.Error())
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		panic("secret: gcm: " + err.Error())
	}
	return &Cipher{aead: aead}
}

// Encrypt возвращает зашифрованное значение (с префиксом). Пустая строка — «не задано»,
// не шифруется (сохраняет сентинел пустоты для проверок «ключ задан» на ciphertext).
func (c *Cipher) Encrypt(plain string) string {
	if c == nil || plain == "" {
		return plain
	}
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		panic("secret: rand: " + err.Error())
	}
	ct := c.aead.Seal(nonce, nonce, []byte(plain), nil)
	return prefix + base64.StdEncoding.EncodeToString(ct)
}

// Decrypt разворачивает значение. Значение без префикса возвращается как есть
// (legacy-plaintext). Нерасшифровываемое (сменился секрет) → "" (fail-closed: трактуем как
// «не задано», пользователь перевведёт), чтобы не отдавать наружу мусор/ciphertext.
func (c *Cipher) Decrypt(stored string) string {
	if !strings.HasPrefix(stored, prefix) {
		return stored
	}
	if c == nil {
		return ""
	}
	raw, err := base64.StdEncoding.DecodeString(stored[len(prefix):])
	if err != nil || len(raw) < c.aead.NonceSize() {
		return ""
	}
	nonce, ct := raw[:c.aead.NonceSize()], raw[c.aead.NonceSize():]
	plain, err := c.aead.Open(nil, nonce, ct, nil)
	if err != nil {
		return ""
	}
	return string(plain)
}
