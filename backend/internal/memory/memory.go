// Package memory — личная память пользователя: атомарные markdown-заметки в git-репо.
//
// Источник истины — .md-файлы (YAML-frontmatter + тело) в git working-copy на хосте;
// durability делегирована git-remote (любому: GitHub / self-hosted / локальный bare).
// SQLite-индекса нет: при личных объёмах read-path сканирует файлы клона напрямую (индекс
// окупается лишь на масштабе — добавляется отдельно, без миграции модели).
//
// Все операции сериализованы одним мьютексом: запись редкая, git-команды на одном рабочем
// дереве не параллелятся. Push синхронный внутри вызова — между commit и push данные живут
// только на эфемерном диске, поэтому «сохранено» возвращается лишь после успешного push.
package memory

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

// ErrDisabled — фича выключена (не задан memory.remote).
var ErrDisabled = errors.New("memory: remote not configured")

// noteTypes — допустимые типы заметок; неизвестный тип нормализуется в дефолтный.
var noteTypes = map[string]bool{
	"idea": true, "decision": true, "insight": true,
	"todo": true, "question": true, "reference": true,
}

const defaultType = "idea"

// Config — параметры памяти.
type Config struct {
	// Remote — git-remote (источник истины). Пусто — фича выключена.
	Remote string
	// Dir — рабочая копия на хосте (git working clone). Ленивый clone на первом обращении.
	Dir string
	// SSHKey — путь к приватному SSH-ключу для git@-remote (без пароля). Пусто — SSH-настройки
	// хоста (~/.ssh). Для https-remote игнорируется.
	SSHKey string
}

// Note — одна заметка памяти.
type Note struct {
	ID      string
	Title   string
	Body    string
	Type    string
	Tags    []string
	Session string
	Created string // дата ISO (YYYY-MM-DD)
	Updated string
}

// frontmatter — YAML-заголовок .md-файла (round-trip модель хранения).
type frontmatter struct {
	ID      string   `yaml:"id"`
	Title   string   `yaml:"title"`
	Type    string   `yaml:"type"`
	Tags    []string `yaml:"tags,omitempty"`
	Session string   `yaml:"session,omitempty"`
	Created string   `yaml:"created"`
	Updated string   `yaml:"updated"`
}

// Service — ядро памяти: git-хранилище + чтение/запись заметок.
type Service struct {
	cfg    Config
	mu     sync.Mutex
	cloned bool
}

// NewService собирает Service. Клон не создаётся до первого обращения (lazy).
func NewService(cfg Config) *Service { return &Service{cfg: cfg} }

// Enabled — включена ли память (задан remote).
func (s *Service) Enabled() bool { return s.cfg.Remote != "" }

// Create записывает заметку, коммитит и синхронно пушит в remote. Возвращает
// нормализованную заметку и SHA коммита.
func (s *Service) Create(ctx context.Context, n Note) (Note, string, error) {
	if !s.Enabled() {
		return Note{}, "", ErrDisabled
	}
	n = normalize(n)

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureCloneLocked(ctx); err != nil {
		return Note{}, "", err
	}

	rel := notePath(n)
	abs := filepath.Join(s.cfg.Dir, rel)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return Note{}, "", fmt.Errorf("memory: mkdir: %w", err)
	}
	// ponytail: last-write-wins при совпадении id (та же дата + тот же slug title).
	if err := os.WriteFile(abs, renderNote(n), 0o644); err != nil {
		return Note{}, "", fmt.Errorf("memory: write %s: %w", rel, err)
	}

	sha, err := s.commitPushLocked(ctx, rel, "memory: note "+n.ID)
	if err != nil {
		return Note{}, "", err
	}
	return n, sha, nil
}

// List возвращает заметки, при непустом query — отфильтрованные по подстроке
// (title/body/tags, регистронезависимо), отсортированные от новых к старым.
func (s *Service) List(ctx context.Context, query string) ([]Note, error) {
	if !s.Enabled() {
		return nil, ErrDisabled
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureCloneLocked(ctx); err != nil {
		return nil, err
	}
	notes, err := s.scanLocked()
	if err != nil {
		return nil, err
	}
	q := strings.ToLower(strings.TrimSpace(query))
	if q != "" {
		filtered := notes[:0]
		for _, n := range notes {
			if matches(n, q) {
				filtered = append(filtered, n)
			}
		}
		notes = filtered
	}
	sort.Slice(notes, func(i, j int) bool {
		if notes[i].Updated != notes[j].Updated {
			return notes[i].Updated > notes[j].Updated
		}
		return notes[i].ID > notes[j].ID
	})
	return notes, nil
}

// Get возвращает заметку по id (store.ErrNotFound-эквивалент — ErrNotFound ниже).
func (s *Service) Get(ctx context.Context, id string) (Note, error) {
	if !s.Enabled() {
		return Note{}, ErrDisabled
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureCloneLocked(ctx); err != nil {
		return Note{}, err
	}
	notes, err := s.scanLocked()
	if err != nil {
		return Note{}, err
	}
	for _, n := range notes {
		if n.ID == id {
			return n, nil
		}
	}
	return Note{}, ErrNotFound
}

// ErrNotFound — заметка не найдена.
var ErrNotFound = errors.New("memory: note not found")

// ensureCloneLocked лениво поднимает рабочую копию: existing .git → используем, иначе
// git clone --depth 1. --depth 1 роняет локальную историю (её видно из remote) — для
// working-copy этого достаточно. Идентичность коммитов настраивается локально в клоне.
func (s *Service) ensureCloneLocked(ctx context.Context) error {
	if s.cloned {
		return nil
	}
	if isGitRepo(s.cfg.Dir) {
		s.cloned = true
		return s.configIdentityLocked(ctx)
	}
	if err := os.MkdirAll(filepath.Dir(s.cfg.Dir), 0o755); err != nil {
		return fmt.Errorf("memory: mkdir parent: %w", err)
	}
	if _, err := s.git(ctx, "", "clone", "--depth", "1", s.cfg.Remote, s.cfg.Dir); err != nil {
		return fmt.Errorf("memory: clone: %w", err)
	}
	s.cloned = true
	return s.configIdentityLocked(ctx)
}

// configIdentityLocked задаёт локальную git-идентичность клона (нужна для commit).
func (s *Service) configIdentityLocked(ctx context.Context) error {
	if _, err := s.git(ctx, s.cfg.Dir, "config", "user.email", "brigade@localhost"); err != nil {
		return err
	}
	_, err := s.git(ctx, s.cfg.Dir, "config", "user.name", "brigade")
	return err
}

// commitPushLocked коммитит rel и синхронно пушит. При отклонении push (кто-то опередил)
// делает pull --rebase и повторяет push один раз. Возвращает SHA HEAD.
func (s *Service) commitPushLocked(ctx context.Context, rel, msg string) (string, error) {
	if _, err := s.git(ctx, s.cfg.Dir, "add", "--", rel); err != nil {
		return "", err
	}
	// commit может сообщить «nothing to commit» (перезапись идентичным содержимым) — это не
	// ошибка: используем текущий HEAD.
	if out, err := s.git(ctx, s.cfg.Dir, "commit", "-m", msg); err != nil {
		if !bytes.Contains(out, []byte("nothing to commit")) {
			return "", err
		}
	}
	if _, err := s.git(ctx, s.cfg.Dir, "push", "origin", "HEAD"); err != nil {
		// Опередили — интегрируем remote и пробуем ещё раз (атомарные пофайловые заметки
		// делают реальный конфликт редким; при нём rebase упадёт и вернёт ошибку наверх).
		if _, perr := s.git(ctx, s.cfg.Dir, "pull", "--rebase", "origin", "HEAD"); perr != nil {
			return "", perr
		}
		if _, err := s.git(ctx, s.cfg.Dir, "push", "origin", "HEAD"); err != nil {
			return "", err
		}
	}
	out, err := s.git(ctx, s.cfg.Dir, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// scanLocked читает все *.md рабочей копии в заметки (пропуская .git).
func (s *Service) scanLocked() ([]Note, error) {
	var notes []Note
	err := filepath.WalkDir(s.cfg.Dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".md") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		n, ok := parseNote(data)
		if !ok {
			return nil // файл без валидного frontmatter — не заметка (README и т.п.)
		}
		notes = append(notes, n)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("memory: scan: %w", err)
	}
	return notes, nil
}

// --- сериализация заметки ---

var fmDelim = []byte("---\n")

// parseNote разбирает .md-файл: `---\n<yaml>\n---\n<body>`. Возвращает ok=false, если
// формат не распознан.
func parseNote(data []byte) (Note, bool) {
	if !bytes.HasPrefix(data, fmDelim) {
		return Note{}, false
	}
	rest := data[len(fmDelim):]
	end := bytes.Index(rest, []byte("\n---"))
	if end < 0 {
		return Note{}, false
	}
	var fm frontmatter
	if err := yaml.Unmarshal(rest[:end], &fm); err != nil || fm.ID == "" {
		return Note{}, false
	}
	body := rest[end+len("\n---"):]
	body = bytes.TrimPrefix(body, []byte("\n"))
	body = bytes.TrimLeft(body, "\n")
	return Note{
		ID: fm.ID, Title: fm.Title, Type: fm.Type, Tags: fm.Tags,
		Session: fm.Session, Created: fm.Created, Updated: fm.Updated,
		Body: strings.TrimRight(string(body), "\n"),
	}, true
}

// renderNote сериализует заметку в .md-файл с frontmatter.
func renderNote(n Note) []byte {
	fm := frontmatter{
		ID: n.ID, Title: n.Title, Type: n.Type, Tags: n.Tags,
		Session: n.Session, Created: n.Created, Updated: n.Updated,
	}
	head, _ := yaml.Marshal(fm)
	var b bytes.Buffer
	b.Write(fmDelim)
	b.Write(head)
	b.WriteString("---\n\n")
	b.WriteString(strings.TrimRight(n.Body, "\n"))
	b.WriteByte('\n')
	return b.Bytes()
}

// --- нормализация и утилиты ---

var slugRe = regexp.MustCompile(`[^a-z0-9]+`)

// normalize заполняет отсутствующие поля: тип по умолчанию, даты, id из даты+slug(title).
func normalize(n Note) Note {
	n.Type = strings.TrimSpace(strings.ToLower(n.Type))
	if !noteTypes[n.Type] {
		n.Type = defaultType
	}
	today := time.Now().Format("2006-01-02")
	if n.Created == "" {
		n.Created = today
	}
	n.Updated = today
	if n.ID == "" {
		n.ID = today + "-" + slug(n.Title)
	}
	return n
}

// slug приводит заголовок к безопасному фрагменту id; пустой → "note".
func slug(title string) string {
	s := slugRe.ReplaceAllString(strings.ToLower(title), "-")
	s = strings.Trim(s, "-")
	if s == "" {
		return "note"
	}
	if len(s) > 60 {
		s = strings.Trim(s[:60], "-")
	}
	return s
}

// notePath — путь файла заметки относительно корня репо: <type>s/<id>.md.
func notePath(n Note) string {
	return filepath.Join(n.Type+"s", n.ID+".md")
}

// matches — попадает ли заметка под поисковую подстроку (уже в нижнем регистре).
func matches(n Note, q string) bool {
	if strings.Contains(strings.ToLower(n.Title), q) || strings.Contains(strings.ToLower(n.Body), q) {
		return true
	}
	for _, t := range n.Tags {
		if strings.Contains(strings.ToLower(t), q) {
			return true
		}
	}
	return false
}

func isGitRepo(dir string) bool {
	info, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil && info.IsDir()
}

// git выполняет git-команду в dir (пусто — без рабочего каталога). Интерактивный запрос
// кредов отключён (GIT_TERMINAL_PROMPT=0) — недоступный remote падает сразу. При заданном
// SSHKey подставляет GIT_SSH_COMMAND для доступа по git@-remote указанным ключом.
func (s *Service) git(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	env := append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if s.cfg.SSHKey != "" {
		// IdentitiesOnly — не предлагать чужие ключи из ssh-agent (иначе можно упереться в
		// MaxAuthTries); accept-new — доверяем host key при первом коннекте (TOFU) и
		// отвергаем при его смене. ponytail: TOFU достаточно для автоматизации; для строгой
		// проверки заранее засей known_hosts хоста — тогда accept-new ничего не добавит.
		env = append(env, fmt.Sprintf(
			"GIT_SSH_COMMAND=ssh -i %q -o IdentitiesOnly=yes -o StrictHostKeyChecking=accept-new",
			s.cfg.SSHKey))
	}
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, bytes.TrimSpace(out))
	}
	return out, nil
}
