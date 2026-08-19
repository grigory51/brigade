// Package codexlogin запускает официальный device-code login Codex без передачи
// ChatGPT-пароля brigade. Результирующий auth.json сразу уходит в зашифрованный store.
package codexlogin

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/uuid"
)

type Store interface {
	SetCodexAuthJSON(context.Context, string, string) error
}

type Runner interface {
	Run(context.Context, string, io.Writer) ([]byte, error)
}

type Login struct{ ID, Status, Output, Error string }

type attempt struct {
	login  Login
	userID string
	save   func(context.Context, string) error
	cancel context.CancelFunc
	output bytes.Buffer
}

type Service struct {
	store    Store
	mu       sync.Mutex
	attempts map[string]*attempt
	runner   Runner
}

func New(store Store, runner Runner) *Service {
	return &Service{store: store, runner: runner, attempts: map[string]*attempt{}}
}

type LocalRunner struct{}

func (LocalRunner) Run(ctx context.Context, _ string, output io.Writer) ([]byte, error) {
	home, err := os.MkdirTemp("", "brigade-codex-login-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(home)
	cmd := exec.CommandContext(ctx, "codex", "login", "--device-auth")
	cmd.Env = append(os.Environ(), "CODEX_HOME="+home)
	cmd.Stdout, cmd.Stderr = output, output
	if err := cmd.Run(); err != nil {
		return nil, err
	}
	return os.ReadFile(filepath.Join(home, "auth.json"))
}

func (s *Service) Start(userID string) Login {
	return s.StartWithSave(userID, func(ctx context.Context, data string) error {
		return s.store.SetCodexAuthJSON(ctx, userID, data)
	})
}

// StartWithSave запускает тот же device login, но сохраняет результат в выбранное
// подключение агента вместо legacy singleton-настроек пользователя.
func (s *Service) StartWithSave(userID string, save func(context.Context, string) error) Login {
	s.mu.Lock()
	for _, current := range s.attempts {
		if current.userID == userID && current.login.Status == "pending" {
			current.cancel()
		}
	}
	// Device code живёт 15 минут. После этого ждать процесс бессмысленно, а вечный
	// pending скрывает сетевые и runtime-сбои на удалённой инсталляции.
	ctx, cancel := context.WithTimeout(context.Background(), 16*time.Minute)
	a := &attempt{login: Login{ID: uuid.NewString(), Status: "pending"}, userID: userID, save: save, cancel: cancel}
	s.attempts[a.login.ID] = a
	login := a.login
	s.mu.Unlock()
	go s.run(ctx, userID, a)
	return login
}

func (s *Service) Get(userID, id string) (Login, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.attempts[id]
	if a == nil || a.userID != userID {
		return Login{}, errors.New("codex login not found")
	}
	out := a.login
	out.Output = a.output.String()
	return out, nil
}

func (s *Service) Cancel(userID, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.attempts[id]
	if a == nil || a.userID != userID {
		return errors.New("codex login not found")
	}
	a.cancel()
	return nil
}

func (s *Service) run(ctx context.Context, userID string, a *attempt) {
	log.Printf("codex login %s: started", a.login.ID)
	data, err := s.runner.Run(ctx, userID, &lockedWriter{service: s, attempt: a})
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			err = errors.New("Codex device login timed out; check outbound access to auth.openai.com from the agent container")
		}
		log.Printf("codex login %s: failed: %v", a.login.ID, err)
		s.finish(a, "failed", err)
		return
	}
	if err := a.save(context.Background(), string(data)); err != nil {
		log.Printf("codex login %s: persist failed: %v", a.login.ID, err)
		s.finish(a, "failed", err)
		return
	}
	log.Printf("codex login %s: completed", a.login.ID)
	s.finish(a, "completed", nil)
}

func (s *Service) finish(a *attempt, status string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a.login.Status = status
	if err != nil {
		a.login.Error = fmt.Sprintf("%v", err)
	}
}

type lockedWriter struct {
	service *Service
	attempt *attempt
}

func (w *lockedWriter) Write(p []byte) (int, error) {
	w.service.mu.Lock()
	defer w.service.mu.Unlock()
	return w.attempt.output.Write(p)
}
