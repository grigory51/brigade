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
	"log"
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

// Слои памяти (layered memory): semantic — атомарный дистиллированный факт (дефолт);
// episodic — саммари сессии (что было сделано). Неизвестный слой → semantic.
const (
	layerSemantic = "semantic"
	layerEpisodic = "episodic"
	defaultLayer  = layerSemantic
)

var noteLayers = map[string]bool{layerSemantic: true, layerEpisodic: true}

// SettingsSource отдаёт пер-юзерные настройки памяти (remote, уже расшифрованный).
// Реализуется *store.Store.
type SettingsSource interface {
	GetUserSettings(ctx context.Context, userID string) (store.UserSettings, error)
}

// AgentKeyProvider выдаёт per-user приватный SSH-ключ агента (генерируя пару при первом
// обращении). Это тот же ключ, что brigade подкладывает в контейнер сессии; память использует
// его для git@-remote, чтобы пользователю не требовалось отдельно задавать ключ памяти —
// достаточно один раз добавить публичный ключ агента в git-хост. Реализуется auth.Service.
type AgentKeyProvider interface {
	EnsureAgentSSHKey(ctx context.Context, userID string) (privatePEM, publicKey string, err error)
}

// Note — одна заметка памяти (живёт внутри темы/подтемы).
type Note struct {
	ID      string
	Title   string
	Body    string
	Type    string
	Tags    []string
	Session string
	Created string // дата ISO (YYYY-MM-DD)
	Updated string
	Layer   string // semantic | episodic (legacy-слой, в темо-UI не используется)
	TopicID string // тема-владелец; выводится из пути файла ("general" — legacy/«Общее»)
	Sub     string // подтема внутри темы
	From    string // человекочитаемый провенанс («чат: …», «вручную»)
}

// Topic — тема памяти («блокнот»): обзор-synthesis + заметки по подтемам. Производные поля
// (NoteCount/ChatCount/Recent) заполняются при чтении, в _topic.md не хранятся.
type Topic struct {
	ID        string
	Name      string
	Color     string
	Initial   string
	Synthesis string
	Subs      []string
	Created   string
	Updated   string
	NoteCount int
	ChatCount int
	Recent    []Note
}

// frontmatter — YAML-заголовок .md-файла заметки (round-trip модель хранения).
type frontmatter struct {
	ID      string   `yaml:"id"`
	Title   string   `yaml:"title"`
	Type    string   `yaml:"type"`
	Layer   string   `yaml:"layer"`
	Tags    []string `yaml:"tags,omitempty"`
	Session string   `yaml:"session,omitempty"`
	Sub     string   `yaml:"sub,omitempty"`
	From    string   `yaml:"from,omitempty"`
	Created string   `yaml:"created"`
	Updated string   `yaml:"updated"`
}

// topicFrontmatter — YAML-заголовок _topic.md (мета темы; тело файла = synthesis-обзор).
type topicFrontmatter struct {
	ID      string   `yaml:"id"`
	Name    string   `yaml:"name"`
	Color   string   `yaml:"color"`
	Initial string   `yaml:"initial"`
	Subs    []string `yaml:"subs,omitempty"`
	Created string   `yaml:"created"`
	Updated string   `yaml:"updated"`
}

// generalTopicID — виртуальная тема «Общее»: собирает legacy-заметки (созданные до тем,
// лежат в <type>s/ и sessions/) и заметки без явной темы.
const generalTopicID = "general"

const generalTopicName = "Общее"

// topicColors — палитра акцентов тем (из дизайн-хендоффа). Новой теме без явного цвета
// назначается по кругу (детерминированно от числа существующих тем).
var topicColors = []string{
	"#c96442", // терракот
	"#d9a441", // янтарь
	"#6fa564", // зелёный
	"#7c9fd6", // синий
	"#b98cd1", // фиолетовый
	"#a8a49a", // нейтральный
}

// Service — ядро памяти: пер-юзерные git-хранилища + чтение/запись заметок.
type Service struct {
	baseDir   string           // корень пер-юзерных рабочих копий: <baseDir>/<userID>/...
	settings  SettingsSource   // источник пер-юзерных настроек (remote)
	agentKeys AgentKeyProvider // источник per-user SSH-ключа агента для git@-remote (может быть nil)
	mu        sync.Mutex
}

// NewService собирает Service. baseDir — база пер-юзерных клонов; settings — источник
// пер-юзерных настроек (store); agentKeys — источник per-user SSH-ключа агента для доступа к
// git@-remote (nil — без ключа, только публичные/https-remote).
func NewService(baseDir string, settings SettingsSource, agentKeys AgentKeyProvider) *Service {
	return &Service{baseDir: baseDir, settings: settings, agentKeys: agentKeys}
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
	sha, err := s.commitPushLocked(ctx, sp, "memory: note "+n.ID, rel)
	if err != nil {
		return Note{}, "", err
	}
	return n, sha, nil
}

// CreateNoteInTopic создаёт заметку в теме по её ИМЕНИ: тема резолвится (по slug/имени) или
// создаётся, если её нет; пустое имя (или «Общее») кладёт заметку в виртуальную «Общее». Нужна
// агент-пути (skill /note): агент задаёт человекочитаемое имя темы, а не её id. Композирует
// публичные методы без вложенной блокировки (каждый берёт mu сам).
func (s *Service) CreateNoteInTopic(ctx context.Context, userID, topicName string, n Note) (Note, string, error) {
	topicID, err := s.ensureTopicID(ctx, userID, topicName)
	if err != nil {
		return Note{}, "", err
	}
	n.TopicID = topicID
	return s.Create(ctx, userID, n)
}

// ensureTopicID резолвит имя темы в её id, создавая тему при отсутствии. Пусто/«Общее» →
// general (виртуальная тема legacy-заметок, её не создаём). Существующую находит по совпадению
// id==slug(name) либо имени (регистронезависимо).
func (s *Service) ensureTopicID(ctx context.Context, userID, name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || strings.EqualFold(name, generalTopicName) {
		return generalTopicID, nil
	}
	topics, err := s.ListTopics(ctx, userID, "")
	if err != nil {
		return "", err
	}
	slug := topicSlug(name)
	for _, t := range topics {
		if t.ID == slug || strings.EqualFold(t.Name, name) {
			return t.ID, nil
		}
	}
	t, err := s.CreateTopic(ctx, userID, name, "")
	if err != nil {
		return "", err
	}
	return t.ID, nil
}

// Sync подтягивает свежие изменения памяти с origin в локальную рабочую копию
// (git pull --rebase). Нужен, чтобы видеть правки, сделанные из ДРУГОГО инстанса brigade: у
// каждого инстанса свой локальный клон, а read-путь читает клон как есть. --rebase (не reset)
// сохраняет незапушенные локальные коммиты (если push ранее сорвался). Пустой remote — no-op;
// конфликт rebase откатывается (иначе клон застрянет в rebase-in-progress).
func (s *Service) Sync(ctx context.Context, userID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sp, err := s.prepareLocked(ctx, userID)
	if err != nil {
		return err
	}
	out, err := s.git(ctx, sp.repoDir, sp.keyPath, "pull", "--rebase", "origin", "HEAD")
	if err != nil {
		// Пустой remote (ни одного коммита) — синхронизировать нечего, не ошибка.
		if bytes.Contains(out, []byte("couldn't find remote ref")) ||
			bytes.Contains(out, []byte("no such ref")) {
			return nil
		}
		// Конфликт rebase оставил бы клон в rebase-in-progress (проходит repoHealthy, но
		// следующие операции падают) — откатываем и отдаём ошибку наверх.
		_, _ = s.git(ctx, sp.repoDir, sp.keyPath, "rebase", "--abort")
		return fmt.Errorf("memory: sync: %w", err)
	}
	return nil
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
	files, err := scanNotes(sp.repoDir)
	if err != nil {
		return nil, err
	}
	notes := notesOf(files)
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
	files, err := scanNotes(sp.repoDir)
	if err != nil {
		return Note{}, err
	}
	for _, f := range files {
		if f.ID == id {
			return f.Note, nil
		}
	}
	return Note{}, ErrNotFound
}

// --- темы ---

// ListTopics возвращает темы пользователя с производными (count/updated/chats/recent),
// отсортированные от свежих к старым. При непустом query — фильтр по имени/обзору/заметкам.
func (s *Service) ListTopics(ctx context.Context, userID, query string) ([]Topic, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sp, err := s.prepareLocked(ctx, userID)
	if err != nil {
		return nil, err
	}
	files, err := scanNotes(sp.repoDir)
	if err != nil {
		return nil, err
	}
	metas, err := scanTopics(sp.repoDir)
	if err != nil {
		return nil, err
	}
	topics := assembleTopics(metas, files)
	q := strings.ToLower(strings.TrimSpace(query))
	if q != "" {
		filtered := topics[:0]
		for _, t := range topics {
			if topicMatches(t, q) {
				filtered = append(filtered, t)
			}
		}
		topics = filtered
	}
	sort.Slice(topics, func(i, j int) bool {
		if topics[i].Updated != topics[j].Updated {
			return topics[i].Updated > topics[j].Updated
		}
		return topics[i].Name < topics[j].Name
	})
	return topics, nil
}

// GetTopic возвращает тему с полным обзором и все её заметки (от свежих к старым).
func (s *Service) GetTopic(ctx context.Context, userID, id string) (Topic, []Note, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sp, err := s.prepareLocked(ctx, userID)
	if err != nil {
		return Topic{}, nil, err
	}
	files, err := scanNotes(sp.repoDir)
	if err != nil {
		return Topic{}, nil, err
	}
	metas, err := scanTopics(sp.repoDir)
	if err != nil {
		return Topic{}, nil, err
	}
	var notes []Note
	for _, f := range files {
		if f.TopicID == id {
			notes = append(notes, f.Note)
		}
	}
	topic, found := Topic{}, false
	for _, m := range metas {
		if m.ID == id {
			topic, found = m, true
			break
		}
	}
	if !found {
		if len(notes) == 0 {
			return Topic{}, nil, ErrNotFound
		}
		topic = virtualTopicMeta(id) // тема без _topic.md (напр. «Общее» из legacy-заметок)
	}
	deriveTopic(&topic, notes)
	return topic, notes, nil
}

// CreateTopic создаёт тему (имя + цвет). id выводится из имени (уникализируется суффиксом),
// initial — заглавная первая буква, стартовая подтема — «Общее».
func (s *Service) CreateTopic(ctx context.Context, userID, name, color string) (Topic, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	name = strings.TrimSpace(name)
	if name == "" {
		return Topic{}, fmt.Errorf("memory: topic name required")
	}
	sp, err := s.prepareLocked(ctx, userID)
	if err != nil {
		return Topic{}, err
	}
	metas, err := scanTopics(sp.repoDir)
	if err != nil {
		return Topic{}, err
	}
	taken := make(map[string]bool, len(metas))
	for _, m := range metas {
		taken[m.ID] = true
	}
	id := uniqueTopicID(topicSlug(name), taken)
	if color == "" {
		color = topicColors[len(metas)%len(topicColors)]
	}
	today := time.Now().Format("2006-01-02")
	t := Topic{
		ID: id, Name: name, Color: color, Initial: initialOf(name),
		Subs: []string{"Общее"}, Created: today, Updated: today,
	}
	rel := filepath.Join("topics", id, topicMetaFile)
	abs := filepath.Join(sp.repoDir, rel)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return Topic{}, fmt.Errorf("memory: mkdir topic: %w", err)
	}
	if err := os.WriteFile(abs, renderTopic(t), 0o644); err != nil {
		return Topic{}, fmt.Errorf("memory: write topic: %w", err)
	}
	if _, err := s.commitPushLocked(ctx, sp, "memory: topic "+id, rel); err != nil {
		return Topic{}, err
	}
	return t, nil
}

// UpdateTopicOverview перезаписывает synthesis-обзор темы. Для виртуальной «Общее» (или темы
// без _topic.md) — материализует _topic.md.
func (s *Service) UpdateTopicOverview(ctx context.Context, userID, id, synthesis string) (Topic, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sp, err := s.prepareLocked(ctx, userID)
	if err != nil {
		return Topic{}, err
	}
	metas, err := scanTopics(sp.repoDir)
	if err != nil {
		return Topic{}, err
	}
	topic := Topic{}
	for _, m := range metas {
		if m.ID == id {
			topic = m
			break
		}
	}
	if topic.ID == "" {
		topic = virtualTopicMeta(id) // материализуем ранее виртуальную тему
	}
	topic.Synthesis = strings.TrimSpace(synthesis)
	topic.Updated = time.Now().Format("2006-01-02")
	if topic.Created == "" {
		topic.Created = topic.Updated
	}
	rel := filepath.Join("topics", id, topicMetaFile)
	abs := filepath.Join(sp.repoDir, rel)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return Topic{}, fmt.Errorf("memory: mkdir topic: %w", err)
	}
	if err := os.WriteFile(abs, renderTopic(topic), 0o644); err != nil {
		return Topic{}, fmt.Errorf("memory: write topic: %w", err)
	}
	if _, err := s.commitPushLocked(ctx, sp, "memory: overview "+id, rel); err != nil {
		return Topic{}, err
	}
	return topic, nil
}

// UpdateTopic переименовывает тему и/или меняет её цвет (id неизменен). Пустые поля не
// трогаются; initial пересчитывается из нового имени.
func (s *Service) UpdateTopic(ctx context.Context, userID, id, name, color string) (Topic, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sp, err := s.prepareLocked(ctx, userID)
	if err != nil {
		return Topic{}, err
	}
	metas, err := scanTopics(sp.repoDir)
	if err != nil {
		return Topic{}, err
	}
	topic := Topic{}
	for _, m := range metas {
		if m.ID == id {
			topic = m
			break
		}
	}
	if topic.ID == "" {
		topic = virtualTopicMeta(id) // материализуем ранее виртуальную тему (напр. «Общее»)
	}
	if name = strings.TrimSpace(name); name != "" {
		topic.Name = name
		topic.Initial = initialOf(name)
	}
	if color != "" {
		topic.Color = color
	}
	topic.Updated = time.Now().Format("2006-01-02")
	if topic.Created == "" {
		topic.Created = topic.Updated
	}
	rel := filepath.Join("topics", id, topicMetaFile)
	abs := filepath.Join(sp.repoDir, rel)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return Topic{}, fmt.Errorf("memory: mkdir topic: %w", err)
	}
	if err := os.WriteFile(abs, renderTopic(topic), 0o644); err != nil {
		return Topic{}, fmt.Errorf("memory: write topic: %w", err)
	}
	if _, err := s.commitPushLocked(ctx, sp, "memory: topic update "+id, rel); err != nil {
		return Topic{}, err
	}
	return topic, nil
}

// DeleteTopic удаляет тему целиком (каталог topics/<id>/ со всеми заметками). Виртуальную
// «Общее» удалить нельзя — она собирает legacy-заметки, которых нет в topics/general/.
func (s *Service) DeleteTopic(ctx context.Context, userID, id string) (string, error) {
	if id == generalTopicID {
		return "", fmt.Errorf("memory: cannot delete the «Общее» topic")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sp, err := s.prepareLocked(ctx, userID)
	if err != nil {
		return "", err
	}
	rel := filepath.Join("topics", id)
	abs := filepath.Join(sp.repoDir, rel)
	if _, statErr := os.Stat(abs); os.IsNotExist(statErr) {
		return "", ErrNotFound
	}
	if err := os.RemoveAll(abs); err != nil {
		return "", fmt.Errorf("memory: remove topic: %w", err)
	}
	return s.commitPushLocked(ctx, sp, "memory: topic delete "+id, rel)
}

// --- правки заметок ---

// UpdateNote меняет поля заметки на месте (title/body/type/sub). Пустые title/body/type
// оставляют прежнее значение; sub применяется как есть (пустой — снять подтему).
func (s *Service) UpdateNote(ctx context.Context, userID, id, title, body, ntype, sub string) (Note, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sp, err := s.prepareLocked(ctx, userID)
	if err != nil {
		return Note{}, "", err
	}
	f, err := s.findFileLocked(sp, id)
	if err != nil {
		return Note{}, "", err
	}
	n := f.Note
	if title != "" {
		n.Title = title
	}
	if body != "" {
		n.Body = body
	}
	if ntype != "" {
		n.Type = strings.ToLower(strings.TrimSpace(ntype))
		if !noteTypes[n.Type] {
			n.Type = f.Type
		}
	}
	n.Sub = strings.TrimSpace(sub)
	n.Updated = time.Now().Format("2006-01-02")
	abs := filepath.Join(sp.repoDir, f.Rel)
	if err := os.WriteFile(abs, renderNote(n), 0o644); err != nil {
		return Note{}, "", fmt.Errorf("memory: write %s: %w", f.Rel, err)
	}
	sha, err := s.commitPushLocked(ctx, sp, "memory: update "+id, f.Rel)
	if err != nil {
		return Note{}, "", err
	}
	return n, sha, nil
}

// MoveNote переносит заметку в другую тему и/или подтему. При смене темы файл перекладывается
// (topics/<to>/<id>.md), старый путь удаляется; в пределах темы меняется только подтема.
func (s *Service) MoveNote(ctx context.Context, userID, id, toTopic, toSub string) (Note, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sp, err := s.prepareLocked(ctx, userID)
	if err != nil {
		return Note{}, "", err
	}
	f, err := s.findFileLocked(sp, id)
	if err != nil {
		return Note{}, "", err
	}
	n := f.Note
	if toTopic != "" {
		n.TopicID = toTopic
	}
	n.Sub = strings.TrimSpace(toSub)
	n.Updated = time.Now().Format("2006-01-02")
	newRel := notePath(n)
	newAbs := filepath.Join(sp.repoDir, newRel)
	if err := os.MkdirAll(filepath.Dir(newAbs), 0o755); err != nil {
		return Note{}, "", fmt.Errorf("memory: mkdir: %w", err)
	}
	if err := os.WriteFile(newAbs, renderNote(n), 0o644); err != nil {
		return Note{}, "", fmt.Errorf("memory: write %s: %w", newRel, err)
	}
	rels := []string{newRel}
	if newRel != f.Rel {
		if err := os.Remove(filepath.Join(sp.repoDir, f.Rel)); err != nil && !os.IsNotExist(err) {
			return Note{}, "", fmt.Errorf("memory: remove %s: %w", f.Rel, err)
		}
		rels = append(rels, f.Rel)
	}
	sha, err := s.commitPushLocked(ctx, sp, "memory: move "+id, rels...)
	if err != nil {
		return Note{}, "", err
	}
	return n, sha, nil
}

// DeleteNote удаляет заметку (rm .md → commit → push).
func (s *Service) DeleteNote(ctx context.Context, userID, id string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sp, err := s.prepareLocked(ctx, userID)
	if err != nil {
		return "", err
	}
	f, err := s.findFileLocked(sp, id)
	if err != nil {
		return "", err
	}
	if err := os.Remove(filepath.Join(sp.repoDir, f.Rel)); err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("memory: remove %s: %w", f.Rel, err)
	}
	return s.commitPushLocked(ctx, sp, "memory: delete "+id, f.Rel)
}

// findFileLocked ищет заметку по id в рабочей копии, возвращая её вместе с путём.
func (s *Service) findFileLocked(sp space, id string) (noteFile, error) {
	files, err := scanNotes(sp.repoDir)
	if err != nil {
		return noteFile{}, err
	}
	for _, f := range files {
		if f.ID == id {
			return f, nil
		}
	}
	return noteFile{}, ErrNotFound
}

// --- агрегация тем ---

// assembleTopics собирает темы: мета из _topic.md + виртуальные темы для заметок без меты
// (в т.ч. «Общее» из legacy). Заполняет производные поля.
func assembleTopics(metas []Topic, files []noteFile) []Topic {
	byID := map[string]*Topic{}
	var order []string
	for i := range metas {
		m := metas[i]
		byID[m.ID] = &m
		order = append(order, m.ID)
	}
	notesByTopic := map[string][]Note{}
	for _, f := range files {
		notesByTopic[f.TopicID] = append(notesByTopic[f.TopicID], f.Note)
	}
	// Темы без _topic.md, но с заметками (legacy «Общее» или каталог без меты) — синтезируем.
	for tid := range notesByTopic {
		if _, ok := byID[tid]; !ok {
			t := virtualTopicMeta(tid)
			byID[tid] = &t
			order = append(order, tid)
		}
	}
	out := make([]Topic, 0, len(order))
	for _, tid := range order {
		t := byID[tid]
		deriveTopic(t, notesByTopic[tid])
		out = append(out, *t)
	}
	return out
}

// deriveTopic заполняет производные поля темы из её заметок (count/chats/updated/recent),
// дополняя список подтем теми, что реально встречаются в заметках.
func deriveTopic(t *Topic, notes []Note) {
	sort.Slice(notes, func(i, j int) bool {
		if notes[i].Updated != notes[j].Updated {
			return notes[i].Updated > notes[j].Updated
		}
		return notes[i].ID > notes[j].ID
	})
	t.NoteCount = len(notes)
	sessions := map[string]bool{}
	for _, n := range notes {
		if n.Session != "" {
			sessions[n.Session] = true
		}
		if n.Updated > t.Updated {
			t.Updated = n.Updated
		}
	}
	t.ChatCount = len(sessions)
	t.Subs = mergeSubs(t.Subs, notes)
	if len(notes) > 2 {
		t.Recent = append([]Note(nil), notes[:2]...)
	} else {
		t.Recent = append([]Note(nil), notes...)
	}
}

// mergeSubs объединяет подтемы из меты с подтемами, реально встречающимися в заметках,
// сохраняя порядок (мета → новые из заметок).
func mergeSubs(base []string, notes []Note) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, s := range base {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	for _, n := range notes {
		if n.Sub != "" && !seen[n.Sub] {
			seen[n.Sub] = true
			out = append(out, n.Sub)
		}
	}
	return out
}

// virtualTopicMeta синтезирует мету темы без _topic.md: «Общее» для generalTopicID, иначе
// имя = id (каталог topics/<id>/ без меты).
func virtualTopicMeta(id string) Topic {
	if id == generalTopicID {
		return Topic{ID: generalTopicID, Name: generalTopicName, Color: "#a8a49a", Initial: "О"}
	}
	return Topic{ID: id, Name: id, Color: "#a8a49a", Initial: initialOf(id)}
}

// topicMatches — попадает ли тема под поисковую подстроку (имя/обзор/заголовки-тела заметок).
func topicMatches(t Topic, q string) bool {
	if strings.Contains(strings.ToLower(t.Name), q) || strings.Contains(strings.ToLower(t.Synthesis), q) {
		return true
	}
	for _, n := range t.Recent {
		if strings.Contains(strings.ToLower(n.Title), q) || strings.Contains(strings.ToLower(n.Body), q) {
			return true
		}
	}
	return false
}

// topicSlugRe оставляет буквы (в т.ч. кириллицу) и цифры, остальное — в дефис.
var topicSlugRe = regexp.MustCompile(`[^\p{L}\p{N}]+`)

// topicSlug приводит имя темы к id (unicode-буквы сохраняются, поэтому кириллица не теряется).
func topicSlug(name string) string {
	s := topicSlugRe.ReplaceAllString(strings.ToLower(name), "-")
	s = strings.Trim(s, "-")
	if s == "" || s == generalTopicID {
		return "topic-" + s
	}
	if len(s) > 60 {
		s = strings.Trim(s[:60], "-")
	}
	return s
}

// uniqueTopicID добавляет числовой суффикс, если id уже занят.
func uniqueTopicID(id string, taken map[string]bool) string {
	if !taken[id] {
		return id
	}
	for i := 2; ; i++ {
		cand := fmt.Sprintf("%s-%d", id, i)
		if !taken[cand] {
			return cand
		}
	}
}

// initialOf — заглавная первая буква имени (аватар темы); пусто → «•».
func initialOf(name string) string {
	for _, r := range strings.TrimSpace(name) {
		return strings.ToUpper(string(r))
	}
	return "•"
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
	// Для git@/ssh-remote материализуем ключ агента (общий per-user): пользователю не нужно
	// задавать отдельный ключ памяти — тот же публичный ключ, что и у агента, добавляется в
	// git-хост. Для https-remote ключ не нужен (GIT_SSH_COMMAND git не задействует).
	if s.agentKeys != nil && isSSHRemote(set.MemoryRemote) {
		priv, _, err := s.agentKeys.EnsureAgentSSHKey(ctx, userID)
		if err != nil {
			return space{}, fmt.Errorf("memory: agent ssh key: %w", err)
		}
		sp.keyPath = filepath.Join(userDir, "id")
		if err := writeKey(sp.keyPath, priv); err != nil {
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
		if s.repoHealthy(ctx, sp) {
			if _, err := s.git(ctx, sp.repoDir, sp.keyPath, "remote", "set-url", "origin", sp.remote); err != nil {
				return err
			}
			return s.configIdentityLocked(ctx, sp)
		}
		// Битый клон: сорванный git-процесс оставил повреждённый ref/HEAD, из-за чего commit
		// падает (fatal: cannot lock ref 'HEAD'). Клон одноразовый, истина — на remote:
		// сносим и переклонируем. Не чиним ref'ы вручную — переклонирование надёжнее.
		log.Printf("memory: repo %s unhealthy, re-cloning", sp.repoDir)
		if err := os.RemoveAll(sp.repoDir); err != nil {
			return fmt.Errorf("memory: remove broken clone: %w", err)
		}
	}
	if err := os.MkdirAll(filepath.Dir(sp.repoDir), 0o755); err != nil {
		return fmt.Errorf("memory: mkdir parent: %w", err)
	}
	// Полный clone (не --depth 1): shallow-клон на части репозиториев провоцирует конфликт
	// ref'ов при первом коммите; персональные репо памяти малы, экономия глубины не оправдана.
	if _, err := s.git(ctx, "", sp.keyPath, "clone", sp.remote, sp.repoDir); err != nil {
		return fmt.Errorf("memory: clone: %w", err)
	}
	return s.configIdentityLocked(ctx, sp)
}

// repoHealthy проверяет, что клон в рабочем состоянии: HEAD резолвится в коммит либо это
// валидный unborn-symref (пустой remote без коммитов). Повреждённый HEAD/ref (после сорванного
// git-процесса) не проходит обе проверки — такой клон переклонируется.
func (s *Service) repoHealthy(ctx context.Context, sp space) bool {
	if _, err := s.git(ctx, sp.repoDir, sp.keyPath, "rev-parse", "--verify", "--quiet", "HEAD"); err == nil {
		return true
	}
	_, err := s.git(ctx, sp.repoDir, sp.keyPath, "symbolic-ref", "--quiet", "HEAD")
	return err == nil
}

// configIdentityLocked задаёт локальную git-идентичность клона (нужна для commit).
func (s *Service) configIdentityLocked(ctx context.Context, sp space) error {
	if _, err := s.git(ctx, sp.repoDir, sp.keyPath, "config", "user.email", "brigade@localhost"); err != nil {
		return err
	}
	_, err := s.git(ctx, sp.repoDir, sp.keyPath, "config", "user.name", "brigade")
	return err
}

// commitPushLocked стейджит rels (add -A — учитывает создание, изменение и удаление),
// коммитит и синхронно пушит. При отклонении push (кто-то опередил) делает pull --rebase и
// повторяет push один раз. Возвращает SHA HEAD.
func (s *Service) commitPushLocked(ctx context.Context, sp space, msg string, rels ...string) (string, error) {
	dir, key := sp.repoDir, sp.keyPath
	if _, err := s.git(ctx, dir, key, append([]string{"add", "-A", "--"}, rels...)...); err != nil {
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

// isSSHRemote сообщает, требует ли remote SSH-ключа: scp-подобный git@host:path либо ssh://.
// https/http-remote ключ не задействуют.
func isSSHRemote(remote string) bool {
	return strings.HasPrefix(remote, "git@") || strings.HasPrefix(remote, "ssh://")
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

// topicMetaFile — имя файла-меты темы в каталоге topics/<id>/ (frontmatter + synthesis).
const topicMetaFile = "_topic.md"

// noteFile — заметка вместе с её путём относительно корня репо (нужен для update/move/delete).
type noteFile struct {
	Note
	Rel string
}

// scanNotes читает все *.md рабочей копии в заметки (пропуская .git и меты тем _topic.md),
// проставляя TopicID из пути файла.
func scanNotes(dir string) ([]noteFile, error) {
	var out []noteFile
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
		if !strings.HasSuffix(d.Name(), ".md") || d.Name() == topicMetaFile {
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
		rel, _ := filepath.Rel(dir, path)
		n.TopicID = topicIDFromRel(rel)
		out = append(out, noteFile{Note: n, Rel: rel})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("memory: scan: %w", err)
	}
	return out, nil
}

// notesOf извлекает голые Note из noteFile-среза.
func notesOf(files []noteFile) []Note {
	notes := make([]Note, len(files))
	for i, f := range files {
		notes[i] = f.Note
	}
	return notes
}

// topicIDFromRel выводит id темы из пути заметки: topics/<id>/<note>.md → <id>; всё
// остальное (legacy <type>s/, sessions/) → виртуальная «Общее».
func topicIDFromRel(rel string) string {
	parts := strings.Split(filepath.ToSlash(rel), "/")
	if len(parts) >= 3 && parts[0] == "topics" {
		return parts[1]
	}
	return generalTopicID
}

// scanTopics читает мету всех реальных тем (topics/<id>/_topic.md). Виртуальная «Общее»
// сюда не входит — она синтезируется в ListTopics поверх legacy-заметок.
func scanTopics(dir string) ([]Topic, error) {
	entries, err := os.ReadDir(filepath.Join(dir, "topics"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("memory: read topics: %w", err)
	}
	var out []Topic
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, "topics", e.Name(), topicMetaFile))
		if err != nil {
			continue // каталог без _topic.md — не тема
		}
		if t, ok := parseTopic(data); ok {
			out = append(out, t)
		}
	}
	return out, nil
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
	layer := fm.Layer
	if !noteLayers[layer] {
		layer = defaultLayer // старые заметки без поля layer — семантические
	}
	return Note{
		ID: fm.ID, Title: fm.Title, Type: fm.Type, Tags: fm.Tags,
		Session: fm.Session, Created: fm.Created, Updated: fm.Updated,
		Layer: layer, Sub: fm.Sub, From: fm.From,
		Body: strings.TrimRight(string(body), "\n"),
	}, true
}

// renderNote сериализует заметку в .md-файл с frontmatter.
func renderNote(n Note) []byte {
	fm := frontmatter{
		ID: n.ID, Title: n.Title, Type: n.Type, Layer: n.Layer, Tags: n.Tags,
		Session: n.Session, Sub: n.Sub, From: n.From,
		Created: n.Created, Updated: n.Updated,
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

// parseTopic разбирает _topic.md: frontmatter (мета) + тело (synthesis-обзор).
func parseTopic(data []byte) (Topic, bool) {
	if !bytes.HasPrefix(data, fmDelim) {
		return Topic{}, false
	}
	rest := data[len(fmDelim):]
	end := bytes.Index(rest, []byte("\n---"))
	if end < 0 {
		return Topic{}, false
	}
	var fm topicFrontmatter
	if err := yaml.Unmarshal(rest[:end], &fm); err != nil || fm.ID == "" {
		return Topic{}, false
	}
	body := rest[end+len("\n---"):]
	body = bytes.TrimLeft(body, "\n")
	return Topic{
		ID: fm.ID, Name: fm.Name, Color: fm.Color, Initial: fm.Initial,
		Subs: fm.Subs, Created: fm.Created, Updated: fm.Updated,
		Synthesis: strings.TrimRight(string(body), "\n"),
	}, true
}

// renderTopic сериализует тему в _topic.md (frontmatter + тело=synthesis).
func renderTopic(t Topic) []byte {
	fm := topicFrontmatter{
		ID: t.ID, Name: t.Name, Color: t.Color, Initial: t.Initial,
		Subs: t.Subs, Created: t.Created, Updated: t.Updated,
	}
	head, _ := yaml.Marshal(fm)
	var b bytes.Buffer
	b.Write(fmDelim)
	b.Write(head)
	b.WriteString("---\n\n")
	b.WriteString(strings.TrimRight(t.Synthesis, "\n"))
	b.WriteByte('\n')
	return b.Bytes()
}

// --- нормализация и утилиты ---

var slugRe = regexp.MustCompile(`[^a-z0-9]+`)

// normalize заполняет отсутствующие поля: тип по умолчанию, даты, id из даты+slug(title).
func normalize(n Note) Note {
	n.Layer = strings.TrimSpace(strings.ToLower(n.Layer))
	if !noteLayers[n.Layer] {
		n.Layer = defaultLayer
	}
	n.Type = strings.TrimSpace(strings.ToLower(n.Type))
	switch {
	case n.Layer == layerEpisodic:
		// Эпизодические — саммари сессии; тип свободный, дефолт summary. На путь не влияет
		// (episodic всегда в sessions/), поэтому список noteTypes для них не навязываем.
		if n.Type == "" {
			n.Type = "summary"
		}
	case !noteTypes[n.Type]:
		n.Type = defaultType
	}
	if n.TopicID == "" {
		n.TopicID = generalTopicID
	}
	n.Sub = strings.TrimSpace(n.Sub)
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

// notePath — путь файла заметки относительно корня репо. Новые заметки живут внутри темы:
// topics/<topicID>/<id>.md. Пустой TopicID → виртуальная «Общее» (topics/general/). Legacy
// плоские заметки (<type>s/, sessions/) на старых путях только читаются, не перезаписываются
// сюда — их переносит в тему явный MoveNote.
func notePath(n Note) string {
	topic := n.TopicID
	if topic == "" {
		topic = generalTopicID
	}
	return filepath.Join("topics", topic, n.ID+".md")
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
