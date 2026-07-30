// Package runtimecfg — режим исполнения сессий (local или docker) и подключение к докеру,
// настраиваемые из интерфейса.
//
// В серверной инсталляции это свойство инстанса: режим задан конфигом, UI его только
// показывает. В десктопном приложении конфига под рукой у пользователя нет, поэтому режим
// и docker-контекст хранятся отдельным файлом рядом с config.yaml и правятся из настроек.
// Файл читается при старте (см. cmd/brigade/desktop.go) — спавнер создаётся один раз, так
// что смена режима применяется перезапуском приложения.
package runtimecfg

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Settings — то, что лежит в файле десктопных настроек.
type Settings struct {
	// Mode — "local" либо "docker"; пусто — берётся режим из config.yaml.
	Mode string `json:"mode,omitempty"`
	// DockerContext — имя docker-контекста (`docker context ls`), по которому определяется
	// адрес демона. Пусто — контекст по умолчанию (переменные окружения / текущий контекст).
	DockerContext string `json:"dockerContext,omitempty"`
}

// Store читает и пишет файл настроек. Пустой путь — настройки не редактируются
// (серверная инсталляция): Read отдаёт пустые значения, Write возвращает ошибку.
type Store struct{ path string }

// NewStore создаёт хранилище настроек по пути файла. Пустой путь — режим только для чтения.
func NewStore(path string) *Store { return &Store{path: path} }

// Editable — можно ли менять настройки из интерфейса.
func (s *Store) Editable() bool { return s != nil && s.path != "" }

// Read возвращает сохранённые настройки. Отсутствие файла — не ошибка (пустые настройки).
func (s *Store) Read() (Settings, error) {
	if !s.Editable() {
		return Settings{}, nil
	}
	data, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return Settings{}, nil
	}
	if err != nil {
		return Settings{}, fmt.Errorf("runtimecfg: чтение %s: %w", s.path, err)
	}
	var out Settings
	if err := json.Unmarshal(data, &out); err != nil {
		return Settings{}, fmt.Errorf("runtimecfg: разбор %s: %w", s.path, err)
	}
	return out, nil
}

// Write перезаписывает настройки целиком.
func (s *Store) Write(settings Settings) error {
	if !s.Editable() {
		return fmt.Errorf("runtimecfg: режим задан конфигурацией инстанса и из интерфейса не меняется")
	}
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("runtimecfg: сериализация: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return fmt.Errorf("runtimecfg: mkdir: %w", err)
	}
	if err := os.WriteFile(s.path, data, 0o600); err != nil {
		return fmt.Errorf("runtimecfg: запись %s: %w", s.path, err)
	}
	return nil
}

// State — то, что видит интерфейс: сохранённые настройки, фактический режим процесса и
// доступные docker-контексты.
type State struct {
	// Mode — сохранённый режим (то, с чем инстанс поднимется в следующий раз).
	Mode string
	// RunningMode — режим, с которым процесс работает сейчас. Отличается от Mode, пока
	// приложение не перезапущено.
	RunningMode string
	// DockerContext — сохранённый контекст; RunningContext — применённый при старте.
	DockerContext  string
	RunningContext string
	// Editable — можно ли менять режим из интерфейса (десктопная инсталляция).
	Editable bool
	// Contexts — контексты docker CLI на машине (только когда настройки редактируемы).
	Contexts []DockerContext
	// DockerError — почему docker сейчас недоступен (пусто — доступен). Показывается до
	// переключения (демон не установлен/не запущен) и после отката (сохранён docker-режим,
	// но подняться в нём не удалось).
	DockerError string
}

// RestartRequired — сохранённые настройки разошлись с работающими.
func (s State) RestartRequired() bool {
	return s.Mode != s.RunningMode || s.DockerContext != s.RunningContext
}

// Service отдаёт и меняет настройки режима, зная, с чем инстанс фактически запущен.
type Service struct {
	store          *Store
	runningMode    string
	runningContext string
	// probe проверяет доступность docker-демона по адресу контекста. nil — не проверять.
	probe func(ctx context.Context, host string) error
}

// NewService собирает сервис. runningMode/runningContext — то, что применено при старте
// процесса (режим из конфига с учётом сохранённых настроек). probe пингует docker-демон:
// интерфейс должен показать недоступность ДО того, как пользователь переключит режим и
// перезапустит приложение.
func NewService(store *Store, runningMode, runningContext string, probe func(ctx context.Context, host string) error) *Service {
	return &Service{store: store, runningMode: runningMode, runningContext: runningContext, probe: probe}
}

// SetRunningMode переопределяет фактический режим процесса. Вызывается при откате: режим
// сохранён docker'ом, но подняться в нём не удалось, и сессии идут локально.
func (s *Service) SetRunningMode(mode string) { s.runningMode = mode }

// State возвращает текущее состояние настроек.
func (s *Service) State() (State, error) {
	saved, err := s.store.Read()
	if err != nil {
		return State{}, err
	}
	st := State{
		Mode:           s.runningMode,
		RunningMode:    s.runningMode,
		DockerContext:  s.runningContext,
		RunningContext: s.runningContext,
		Editable:       s.store.Editable(),
	}
	if st.Editable {
		if saved.Mode != "" {
			st.Mode = saved.Mode
		}
		st.DockerContext = saved.DockerContext
		st.Contexts = ListDockerContexts()
		st.DockerError = s.dockerError(saved.DockerContext)
	}
	return st, nil
}

// dockerError возвращает причину недоступности docker (пусто — доступен). Проба короткая:
// состояние показывается в настройках, и ждать зависший демон там незачем.
func (s *Service) dockerError(dockerContext string) string {
	if s.probe == nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := s.probe(ctx, DockerHost(dockerContext)); err != nil {
		return err.Error()
	}
	return ""
}

// Set сохраняет режим и docker-контекст. Применятся они при следующем запуске: спавнер
// создаётся один раз на старте, а живые сессии рестарт процесса всё равно не переживают.
func (s *Service) Set(mode, dockerContext string) (State, error) {
	if mode != ModeLocal && mode != ModeDocker {
		return State{}, fmt.Errorf("runtimecfg: неизвестный режим %q", mode)
	}
	if mode == ModeLocal {
		dockerContext = "" // контекст без docker-режима смысла не имеет
	}
	if err := s.store.Write(Settings{Mode: mode, DockerContext: dockerContext}); err != nil {
		return State{}, err
	}
	return s.State()
}

// Режимы исполнения сессий. Совпадают со значениями config.Mode: настройка переопределяет
// именно его.
const (
	ModeLocal  = "local"
	ModeDocker = "docker"
)

// DockerContext — контекст docker CLI: имя и адрес демона.
type DockerContext struct {
	Name string
	Host string
	// Current — контекст, выбранный в docker CLI (`docker context use`).
	Current bool
}

// ListDockerContexts читает контексты docker CLI из ~/.docker. Контексты — файлы метаданных,
// которые пишет сам CLI; собственного API у демона для этого нет. Ошибки чтения отдельных
// контекстов игнорируются: неполный список лучше пустого.
//
// Контекст "default" (переменные окружения / стандартный сокет) присутствует всегда — его
// CLI на диске не хранит.
func ListDockerContexts() []DockerContext {
	home, err := os.UserHomeDir()
	if err != nil {
		return []DockerContext{{Name: "default", Current: true}}
	}
	current := currentContextName(filepath.Join(home, ".docker", "config.json"))

	out := []DockerContext{{Name: "default", Current: current == "default"}}
	metaDir := filepath.Join(home, ".docker", "contexts", "meta")
	entries, err := os.ReadDir(metaDir)
	if err != nil {
		return out
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(metaDir, e.Name(), "meta.json"))
		if err != nil {
			continue
		}
		var meta struct {
			Name      string `json:"Name"`
			Endpoints struct {
				Docker struct {
					Host string `json:"Host"`
				} `json:"docker"`
			} `json:"Endpoints"`
		}
		if json.Unmarshal(data, &meta) != nil || meta.Name == "" {
			continue
		}
		out = append(out, DockerContext{
			Name:    meta.Name,
			Host:    meta.Endpoints.Docker.Host,
			Current: meta.Name == current,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// DockerHost возвращает адрес демона для контекста с таким именем. Пустое имя или
// "default" — пусто: докер-клиент возьмёт адрес из окружения сам.
func DockerHost(name string) string {
	if name == "" || name == "default" {
		return ""
	}
	for _, c := range ListDockerContexts() {
		if c.Name == name {
			return c.Host
		}
	}
	return ""
}

// currentContextName читает выбранный контекст из конфига docker CLI. Пусто/отсутствие —
// "default".
func currentContextName(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return "default"
	}
	var cfg struct {
		CurrentContext string `json:"currentContext"`
	}
	if json.Unmarshal(data, &cfg) != nil || strings.TrimSpace(cfg.CurrentContext) == "" {
		return "default"
	}
	return cfg.CurrentContext
}
