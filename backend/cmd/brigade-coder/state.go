package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode"
)

const maxStateBytes = 1 << 20
const maxRevision = 1<<53 - 1
const stateDirectory = ".brigade/coder"

type ResultKind string

const (
	WebResult      ResultKind = "web"
	TerminalResult ResultKind = "terminal"
)

type Command struct {
	Argv []string `json:"argv" jsonschema:"Executable followed by separate arguments; no shell interpolation"`
	Cwd  string   `json:"cwd" jsonschema:"Existing workspace-relative directory; use . for the workspace root"`
}

type Result struct {
	Name    string     `json:"name" jsonschema:"Unique human-readable result name, at most 100 bytes"`
	Kind    ResultKind `json:"kind" jsonschema:"web or terminal"`
	URL     string     `json:"url,omitempty" jsonschema:"For web only: registered HTTP(S) preview URL; only the Brigade id routing query is allowed; no credentials or fragment"`
	Command *Command   `json:"command,omitempty" jsonschema:"For terminal only: argv and cwd; stored but never executed"`
}

type State struct {
	SchemaVersion int      `json:"schemaVersion"`
	Revision      uint64   `json:"revision"`
	ProjectPath   string   `json:"projectPath"`
	Results       []Result `json:"results"`
}

type stateStore struct {
	workspace string
	root      *os.Root
}

func openStateStore(workspace string) (*stateStore, error) {
	path, err := filepath.Abs(workspace)
	if err != nil {
		return nil, err
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, err
	}
	return &stateStore{workspace: path, root: root}, nil
}

func (s *stateStore) close() { s.root.Close() }

func (s *stateStore) read() (State, error) {
	state := State{SchemaVersion: 1, ProjectPath: ".", Results: []Result{}}
	file, err := s.root.Open(stateDirectory + "/state.json")
	if errors.Is(err, os.ErrNotExist) {
		return state, nil
	}
	if err != nil {
		return State{}, fmt.Errorf("open Coder state: %w", err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxStateBytes+1))
	if err != nil {
		return State{}, fmt.Errorf("read Coder state: %w", err)
	}
	if len(data) > maxStateBytes {
		return State{}, fmt.Errorf("Coder state exceeds %d bytes", maxStateBytes)
	}
	state = State{}
	if err := json.Unmarshal(data, &state); err != nil {
		return State{}, fmt.Errorf("decode Coder state: %w", err)
	}
	if state.SchemaVersion != 1 || state.Results == nil || len(state.Results) > 100 || state.Revision > maxRevision {
		return State{}, fmt.Errorf("unsupported or invalid Coder state")
	}
	seen := make(map[string]bool, len(state.Results))
	if state.ProjectPath == "" {
		state.ProjectPath = "."
	}
	if !filepath.IsLocal(state.ProjectPath) || strings.ContainsRune(state.ProjectPath, 0) {
		return State{}, fmt.Errorf("invalid saved projectPath")
	}
	for _, result := range state.Results {
		if err := result.validate(); err != nil {
			return State{}, fmt.Errorf("invalid saved result: %w", err)
		}
		if seen[result.Name] {
			return State{}, fmt.Errorf("duplicate saved result %q", result.Name)
		}
		seen[result.Name] = true
	}
	return state, nil
}

func (r Result) validate() error {
	if r.Name == "" || r.Name != strings.TrimSpace(r.Name) || len(r.Name) > 100 || strings.ContainsFunc(r.Name, unicode.IsControl) {
		return fmt.Errorf("name must be 1–100 bytes without surrounding whitespace or control characters")
	}
	switch r.Kind {
	case WebResult:
		parsed, err := url.Parse(r.URL)
		if err != nil || len(r.URL) > 2048 || strings.ContainsFunc(r.URL, unicode.IsControl) || parsed == nil ||
			(parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" || parsed.User != nil ||
			parsed.Fragment != "" || strings.Contains(r.URL, "#") || r.Command != nil {
			return fmt.Errorf("web result requires an HTTP(S) URL without credentials or fragment, and no command")
		}
		query, err := url.ParseQuery(parsed.RawQuery)
		if err != nil || len(query) > 1 || (len(query) == 1 && (len(query["id"]) != 1 || query.Get("id") == "")) {
			return fmt.Errorf("only the Brigade id routing query is allowed; never register token-bearing URLs")
		}
	case TerminalResult:
		if r.URL != "" || r.Command == nil || len(r.Command.Argv) == 0 || len(r.Command.Argv) > 64 {
			return fmt.Errorf("terminal result requires command.argv (1–64 arguments), command.cwd and no URL")
		}
		if r.Command.Argv[0] == "" || strings.HasPrefix(r.Command.Argv[0], "-") {
			return fmt.Errorf("argv[0] must identify an executable")
		}
		for _, arg := range r.Command.Argv {
			if len(arg) > 4096 || strings.ContainsRune(arg, 0) {
				return fmt.Errorf("command argument exceeds 4096 bytes or contains NUL")
			}
		}
		if !filepath.IsLocal(r.Command.Cwd) {
			return fmt.Errorf("command.cwd must be workspace-relative")
		}
	default:
		return fmt.Errorf("kind must be web or terminal")
	}
	return nil
}

func (s *stateStore) register(ctx context.Context, result Result) (State, error) {
	if err := result.validate(); err != nil {
		return State{}, err
	}
	if result.Command != nil {
		dir, err := s.root.OpenRoot(result.Command.Cwd)
		if err != nil {
			return State{}, fmt.Errorf("command.cwd must be an existing directory within the workspace: %w", err)
		}
		dir.Close()
	}
	return s.update(ctx, func(state *State) error {
		for i, saved := range state.Results {
			if saved.Name == result.Name {
				state.Results[i] = result
				return nil
			}
		}
		if len(state.Results) == 100 {
			return fmt.Errorf("at most 100 named results are supported")
		}
		state.Results = append(state.Results, result)
		return nil
	})
}

func (s *stateStore) selectProject(ctx context.Context, path string) (State, error) {
	if path == "" {
		path = "."
	}
	if !filepath.IsLocal(path) || strings.ContainsRune(path, 0) {
		return State{}, fmt.Errorf("projectPath must be workspace-relative; use . for the workspace")
	}
	directory, err := s.root.OpenRoot(path)
	if err != nil {
		return State{}, fmt.Errorf("project must be an existing directory inside the workspace: %w", err)
	}
	directory.Close()
	return s.update(ctx, func(state *State) error {
		state.ProjectPath = filepath.ToSlash(filepath.Clean(path))
		return nil
	})
}

func (s *stateStore) update(ctx context.Context, mutate func(*State) error) (State, error) {
	if err := s.root.MkdirAll(stateDirectory, 0700); err != nil {
		return State{}, err
	}
	// Разделяем создание и открытие: конкурентный openat(O_CREAT) на macOS
	// может возвращать ENOENT (golang/go#81246). Lock-файл не удаляется.
	lock, err := s.root.OpenFile(stateDirectory+"/state.lock", os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if errors.Is(err, os.ErrExist) {
		lock, err = s.root.OpenFile(stateDirectory+"/state.lock", os.O_RDWR, 0600)
	}
	if err != nil {
		return State{}, err
	}
	defer lock.Close()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	// Отдельный дескриптор на каждый запрос синхронизирует и goroutines, и два MCP-процесса.
	for {
		if err := ctx.Err(); err != nil {
			return State{}, err
		}
		err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			return State{}, fmt.Errorf("lock Coder state: %w", err)
		}
		select {
		case <-ctx.Done():
			return State{}, ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
	// Закрытие дескриптора освобождает flock, включая аварийное завершение процесса.
	state, err := s.read()
	if err != nil {
		return State{}, err
	}
	if err := mutate(&state); err != nil {
		return State{}, err
	}
	if state.Revision == maxRevision {
		return State{}, fmt.Errorf("Coder state revision limit reached")
	}
	state.Revision++
	data, err := json.Marshal(state)
	if err != nil {
		return State{}, err
	}
	if len(data) > maxStateBytes {
		return State{}, fmt.Errorf("Coder state exceeds %d bytes", maxStateBytes)
	}
	temporary := stateDirectory + "/state-" + rand.Text() + ".tmp"
	file, err := s.root.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return State{}, err
	}
	defer s.root.Remove(temporary)
	_, writeErr := file.Write(data)
	if writeErr == nil {
		writeErr = file.Sync()
	}
	closeErr := file.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return State{}, fmt.Errorf("write Coder state: %w", err)
	}
	if err := s.root.Rename(temporary, stateDirectory+"/state.json"); err != nil {
		return State{}, fmt.Errorf("replace Coder state: %w", err)
	}
	directory, err := s.root.Open(stateDirectory)
	if err != nil {
		return State{}, err
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return State{}, fmt.Errorf("sync Coder state directory: %w", err)
	}
	return state, nil
}
