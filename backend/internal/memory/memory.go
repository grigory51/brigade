// Package memory — личная память пользователя: атомарные markdown-заметки в git-репо.
//
// Источник истины — .md-файлы (YAML-frontmatter + тело) в git working-copy на хосте;
// durability делегирована git-remote. Репозиторий и креды — ПЕР-ЮЗЕРНЫЕ: каждый пользователь
// приносит свой remote и SSH-ключ (настройки в store, значения зашифрованы), поэтому данные
// и доступы изолированы — утечка ключа одного пользователя не открывает данные других.
// SQLite-индекса нет: при личных объёмах read-path сканирует файлы клона напрямую.
//
// Все операции сериализованы одним мьютексом. ponytail: global lock — запись редкая; если
// понадобится параллелизм между пользователями, разбить на per-user локи.
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

	"github.com/grigory51/brigade/backend/internal/store"
)

// ErrDisabled — у пользователя не настроена память (не задан memory-remote).
var ErrDisabled = errors.New("memory: remote not configured")

// ErrNotFound — заметка не найдена.
var ErrNotFound = errors.New("memory: note not found")

// noteTypes — допустимые типы заметок; неизвестный тип нормализуется в дефолтный.
var noteTypes = map[string]bool{
	"idea": true, "decision": true, "insight": true,
	"todo": true, "question": true, "reference": true,
}

const defaultType = "idea"

// SettingsSource отдаёт пер-юзерные настройки памяти (remote + SSH-ключ, уже
// расшифрованные). Реализуется *store.Store.
type SettingsSource interface {
	GetUserSettings(ctx context.Context, userID string) (store.UserSettings, error)
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

// Service — ядро памяти: пер-юзерные git-хранилища + чтение/запись заметок.
type Service struct {
	baseDir  string          // корень пер-юзерных рабочих копий: <baseDir>/<userID>/...
	settings SettingsSource  // источник пер-юзерных настроек (remote, ключ)
	mu       sync.Mutex
}

// NewService собирает Service. baseDir — база пер-юзерных клонов; settings — источник
// пер-юзерных настроек (store).
func NewService(baseDir string, settings SettingsSource) *Service {
	return &Service{baseDir: baseDir, settings: settings}
}

// space — разрешённое пер-юзерное окружение для git-операций.
type space struct {
	remote  string
	repoDir string // рабочая копия: <baseDir>/<userID>/repo
	keyPath string // путь к материализованному SSH-ключу ("" — ключа нет)
}

// Create записывает заметку в репозиторий пользователя, коммитит и синхронно пушит.
func (s *Service) Create(ctx context.Context, userID string, n Note) (Note, string, error) {
	n = normalize(n)
	s.mu.Lock()
	defer s.mu.Unlock()
	sp, err := s.prepareLocked(ctx, userID)
	if err != nil {
		return Note{}, "", err
	}
	rel := notePath(n)
	abs := filepath.Join(sp.repoDir, rel)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return Note{}, "", fmt.Errorf("memory: mkdir: %w", err)
	}
	// ponytail: last-write-wins при совпадении id (та же дата + тот же slug title).
	if err := os.WriteFile(abs, renderNote(n), 0o644); err != nil {
		return Note{}, "", fmt.Errorf("memory: write %s: %w", rel, err)
	}
	sha, err := s.commitPushLocked(ctx, sp, rel, "memory: note "+n.ID)
	if err != nil {
		return Note{}, "", err
	}
	return n, sha, nil
}

// List возвращает заметки пользователя, при непустом query — отфильтрованные по подстроке
// (title/body/tags, регистронезависимо), отсортированные от новых к старым.
func (s *Service) List(ctx context.Context, userID, query string) ([]Note, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sp, err := s.prepareLocked(ctx, userID)
	if err != nil {
		return nil, err
	}
	notes, err := scan(sp.repoDir)
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

// Get возвращает заметку пользователя по id.
func (s *Service) Get(ctx context.Context, userID, id string) (Note, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sp, err := s.prepareLocked(ctx, userID)
	if err != nil {
		return Note{}, err
	}
	notes, err := scan(sp.repoDir)
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

// prepareLocked резолвит настройки пользователя, материализует SSH-ключ и поднимает рабочую
// копию (ленивый clone; на существующем клоне синхронизирует origin — remote мог смениться).
func (s *Service) prepareLocked(ctx context.Context, userID string) (space, error) {
	set, err := s.settings.GetUserSettings(ctx, userID)
	if err != nil {
		return space{}, fmt.Errorf("memory: settings: %w", err)
	}
	if set.MemoryRemote == "" {
		return space{}, ErrDisabled
	}
	userDir := filepath.Join(s.baseDir, userID)
	sp := space{remote: set.MemoryRemote, repoDir: filepath.Join(userDir, "repo")}
	if set.MemorySSHKey != "" {
		sp.keyPath = filepath.Join(userDir, "id")
		if err := writeKey(sp.keyPath, set.MemorySSHKey); err != nil {
			return space{}, err
		}
	}
	if err := s.ensureCloneLocked(ctx, sp); err != nil {
		return space{}, err
	}
	return sp, nil
}

// ensureCloneLocked поднимает рабочую копию: existing .git → синхронизирует origin (remote
// пользователя мог измениться) и идентичность; иначе git clone --depth 1.
func (s *Service) ensureCloneLocked(ctx context.Context, sp space) error {
	if isGitRepo(sp.repoDir) {
		if _, err := s.git(ctx, sp.repoDir, sp.keyPath, "remote", "set-url", "origin", sp.remote); err != nil {
			return err
		}
		return s.configIdentityLocked(ctx, sp)
	}
	if err := os.MkdirAll(filepath.Dir(sp.repoDir), 0o755); err != nil {
		return fmt.Errorf("memory: mkdir parent: %w", err)
	}
	if _, err := s.git(ctx, "", sp.keyPath, "clone", "--depth", "1", sp.remote, sp.repoDir); err != nil {
		return fmt.Errorf("memory: clone: %w", err)
	}
	return s.configIdentityLocked(ctx, sp)
}

// configIdentityLocked задаёт локальную git-идентичность клона (нужна для commit).
func (s *Service) configIdentityLocked(ctx context.Context, sp space) error {
	if _, err := s.git(ctx, sp.repoDir, sp.keyPath, "config", "user.email", "brigade@localhost"); err != nil {
		return err
	}
	_, err := s.git(ctx, sp.repoDir, sp.keyPath, "config", "user.name", "brigade")
	return err
}

// commitPushLocked коммитит rel и синхронно пушит. При отклонении push (кто-то опередил)
// делает pull --rebase и повторяет push один раз. Возвращает SHA HEAD.
func (s *Service) commitPushLocked(ctx context.Context, sp space, rel, msg string) (string, error) {
	dir, key := sp.repoDir, sp.keyPath
	if _, err := s.git(ctx, dir, key, "add", "--", rel); err != nil {
		return "", err
	}
	// commit может сообщить «nothing to commit» (перезапись идентичным содержимым) — это не
	// ошибка: используем текущий HEAD.
	if out, err := s.git(ctx, dir, key, "commit", "-m", msg); err != nil {
		if !bytes.Contains(out, []byte("nothing to commit")) {
			return "", err
		}
	}
	if _, err := s.git(ctx, dir, key, "push", "origin", "HEAD"); err != nil {
		// Опередили — интегрируем remote и пробуем ещё раз (атомарные пофайловые заметки
		// делают реальный конфликт редким; при нём rebase упадёт и вернёт ошибку наверх).
		if _, perr := s.git(ctx, dir, key, "pull", "--rebase", "origin", "HEAD"); perr != nil {
			return "", perr
		}
		if _, err := s.git(ctx, dir, key, "push", "origin", "HEAD"); err != nil {
			return "", err
		}
	}
	out, err := s.git(ctx, dir, key, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// git выполняет git-команду в dir (пусто — без рабочего каталога). Интерактивный запрос
// кредов отключён (GIT_TERMINAL_PROMPT=0). При заданном keyPath подставляет GIT_SSH_COMMAND
// для доступа по git@-remote указанным ключом.
func (s *Service) git(ctx context.Context, dir, keyPath string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	env := append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if keyPath != "" {
		// IdentitiesOnly — не предлагать чужие ключи из ssh-agent; accept-new — доверяем host
		// key при первом коннекте (TOFU) и отвергаем при его смене.
		env = append(env, fmt.Sprintf(
			"GIT_SSH_COMMAND=ssh -i %q -o IdentitiesOnly=yes -o StrictHostKeyChecking=accept-new",
			keyPath))
	}
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, bytes.TrimSpace(out))
	}
	return out, nil
}

// writeKey материализует приватный SSH-ключ в файл с правами 0600 (каталог 0700), гарантируя
// завершающий перевод строки (иначе некоторые парсеры ключа привередничают).
func writeKey(path, content string) error {
	if !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("memory: mkdir key dir: %w", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return fmt.Errorf("memory: write key: %w", err)
	}
	return nil
}

// scan читает все *.md рабочей копии в заметки (пропуская .git).
func scan(dir string) ([]Note, error) {
	var notes []Note
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
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
