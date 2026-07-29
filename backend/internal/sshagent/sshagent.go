// Package sshagent — ssh-agent поверх ключа, который держится ТОЛЬКО в памяти процесса.
//
// Нужен везде, где brigade даёт git/ssh доступ ключом пользователя: в демоне сессии (агент
// подписывает push из своей среды) и на хосте (git-операции с репозиторием личной памяти).
// Раньше в обоих местах ключ материализовался файлом — то есть его мог прочитать любой, кто
// доберётся до этого пути. Теперь наружу уходит только подпись через unix-сокет.
//
// Протокол стандартный (golang.org/x/crypto/ssh/agent), поэтому потребителю не нужна
// настройка: достаточно SSH_AUTH_SOCK.
package sshagent

import (
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"sync"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

// Agent — запущенный ssh-agent с ключом в памяти.
type Agent struct {
	path    string
	keyring agent.Agent

	mu sync.Mutex
	ln net.Listener
}

// Start поднимает агента на сокете path с ключом privatePEM (OpenSSH PEM) и начинает
// обслуживать подключения. Каталог сокета должен существовать.
func Start(path, privatePEM string) (*Agent, error) {
	keyring := agent.NewKeyring()
	if err := addKey(keyring, privatePEM); err != nil {
		return nil, err
	}

	// Остаток от прежнего запуска: unix-сокет не удаляется при падении процесса и занял бы
	// путь.
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("sshagent: socket: %w", err)
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("sshagent: listen: %w", err)
	}
	// Сокет — только владельцу: потребители работают под тем же uid, что и процесс-хозяин.
	if err := os.Chmod(path, 0o600); err != nil {
		_ = ln.Close()
		return nil, fmt.Errorf("sshagent: chmod: %w", err)
	}

	a := &Agent{path: path, keyring: keyring, ln: ln}
	go a.serve(ln)
	return a, nil
}

// Path — путь сокета (значение для SSH_AUTH_SOCK).
func (a *Agent) Path() string { return a.path }

// ReplaceKey меняет ключ в связке — перевыпуск ключа пользователя не должен требовать
// перезапуска процесса-хозяина.
func (a *Agent) ReplaceKey(privatePEM string) error {
	keys, err := a.keyring.List()
	if err != nil {
		return fmt.Errorf("sshagent: list: %w", err)
	}
	for _, k := range keys {
		if err := a.keyring.Remove(k); err != nil {
			return fmt.Errorf("sshagent: remove: %w", err)
		}
	}
	return addKey(a.keyring, privatePEM)
}

// serve обслуживает подключения к сокету. Каждое соединение — отдельная сессия протокола;
// ошибка одного клиента не роняет агента.
func (a *Agent) serve(ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return // listener закрыт (Close) либо среда сворачивается
		}
		go func() {
			defer conn.Close()
			if err := agent.ServeAgent(a.keyring, conn); err != nil && !errors.Is(err, net.ErrClosed) {
				// io.EOF — обычное завершение клиента, шумим только на прочем.
				if err.Error() != "EOF" {
					log.Printf("sshagent: serve: %v", err)
				}
			}
		}()
	}
}

// Close останавливает агента и убирает сокет.
func (a *Agent) Close() {
	a.mu.Lock()
	ln := a.ln
	a.ln = nil
	a.mu.Unlock()
	if ln != nil {
		_ = ln.Close()
	}
	_ = os.Remove(a.path)
}

// addKey разбирает OpenSSH PEM и кладёт ключ в связку.
func addKey(keyring agent.Agent, privatePEM string) error {
	key, err := ssh.ParseRawPrivateKey([]byte(privatePEM))
	if err != nil {
		return fmt.Errorf("sshagent: разбор приватного ключа: %w", err)
	}
	if err := keyring.Add(agent.AddedKey{PrivateKey: key, Comment: "brigade agent key"}); err != nil {
		return fmt.Errorf("sshagent: add: %w", err)
	}
	return nil
}
