package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Архив сессий живёт в том же пер-юзерном git-репозитории, что и заметки, — отдельным
// каталогом archive/. Так у пользователя ОДНО хранилище личных данных: архив переживает
// пересоздание инстанса brigade, уезжает на его remote и читается глазами в git-хостинге.
// В БД архивных сессий больше нет: архивация переносит сессию сюда целиком, а удаление
// сессии удаляет её насовсем.
//
// Раскладка на сессию:
//
//	archive/<id>/session.md     — YAML-frontmatter (мета) + тело: recap от агента
//	archive/<id>/messages.json  — снимок ленты чата (формат acp.Message) для readonly-просмотра
//
// session.md намеренно markdown с frontmatter, как заметки: карточка сессии остаётся
// читаемой в вебе git-хостинга. Лента — JSON: её рендерит клиент, а не человек.
const archiveDir = "archive"

const (
	archiveMetaFile     = "session.md"
	archiveMessagesFile = "messages.json"
)

// ArchivedSession — заархивированная сессия. Messages заполняются только точечным чтением
// (ArchivedMessages): в списке они не нужны, а лента бывает в мегабайты.
type ArchivedSession struct {
	ID        string
	Name      string
	AgentType string
	Kind      string
	Summary   string
	Created   time.Time
	Archived  time.Time
}

// archiveFrontmatter — YAML-заголовок session.md (round-trip модель хранения).
type archiveFrontmatter struct {
	ID        string    `yaml:"id"`
	Name      string    `yaml:"name,omitempty"`
	AgentType string    `yaml:"agent_type,omitempty"`
	Kind      string    `yaml:"kind,omitempty"`
	Created   time.Time `yaml:"created"`
	Archived  time.Time `yaml:"archived"`
}

// ArchiveSession кладёт сессию в архив памяти и синхронно пушит. messages — сырой JSON ленты
// (memory не знает типов ACP и не должен: хранилище тут байтовое). Возврат без ошибки
// означает, что данные уехали на remote — только после этого вызывающий вправе удалять
// сессию из своей БД.
func (s *Service) ArchiveSession(ctx context.Context, userID string, sess ArchivedSession, messages []byte) error {
	if sess.ID == "" {
		return fmt.Errorf("memory: archive: пустой id сессии")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	sp, err := s.prepareLocked(ctx, userID)
	if err != nil {
		return err
	}

	rel := filepath.Join(archiveDir, sess.ID)
	abs := filepath.Join(sp.repoDir, rel)
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return fmt.Errorf("memory: archive mkdir: %w", err)
	}
	if err := os.WriteFile(filepath.Join(abs, archiveMetaFile), renderArchive(sess), 0o644); err != nil {
		return fmt.Errorf("memory: archive write meta: %w", err)
	}
	if err := ensureArchiveJSON(messages); err != nil {
		return err
	}
	// Пустая лента — валидный случай (сессия без единого turn'а): пишем пустой массив,
	// чтобы читатель не разбирал отсутствие файла как ошибку.
	if len(messages) == 0 {
		messages = []byte("[]")
	}
	if err := os.WriteFile(filepath.Join(abs, archiveMessagesFile), messages, 0o644); err != nil {
		return fmt.Errorf("memory: archive write messages: %w", err)
	}

	name := sess.Name
	if name == "" {
		name = sess.ID
	}
	if _, err := s.commitPushLocked(ctx, sp, "archive: "+name, rel); err != nil {
		return err
	}
	return nil
}

// ListArchivedSessions возвращает архив пользователя, новые первыми.
func (s *Service) ListArchivedSessions(ctx context.Context, userID string) ([]ArchivedSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	sp, err := s.prepareLocked(ctx, userID)
	if err != nil {
		return nil, err
	}

	entries, err := os.ReadDir(filepath.Join(sp.repoDir, archiveDir))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // архива ещё не было — пустой список, не ошибка
		}
		return nil, fmt.Errorf("memory: archive list: %w", err)
	}

	out := make([]ArchivedSession, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(sp.repoDir, archiveDir, e.Name(), archiveMetaFile))
		if err != nil {
			continue // каталог без меты — не архивная сессия, пропускаем молча
		}
		sess, ok := parseArchive(data)
		if !ok {
			continue
		}
		out = append(out, sess)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Archived.After(out[j].Archived) })
	return out, nil
}

// ArchivedMessages отдаёт снимок ленты архивной сессии как сырой JSON.
func (s *Service) ArchivedMessages(ctx context.Context, userID, sessionID string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	sp, err := s.prepareLocked(ctx, userID)
	if err != nil {
		return nil, err
	}
	// filepath.Base отсекает попытку вылезти из archive/ подставленным путём.
	data, err := os.ReadFile(filepath.Join(sp.repoDir, archiveDir, filepath.Base(sessionID), archiveMessagesFile))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("memory: archive messages: %w", err)
	}
	return data, nil
}

// DeleteArchivedSession удаляет сессию из архива насовсем: каталог сносится, удаление
// уходит коммитом на remote. Отсутствие каталога — не ошибка (идемпотентно).
func (s *Service) DeleteArchivedSession(ctx context.Context, userID, sessionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	sp, err := s.prepareLocked(ctx, userID)
	if err != nil {
		return err
	}
	rel := filepath.Join(archiveDir, filepath.Base(sessionID))
	if err := os.RemoveAll(filepath.Join(sp.repoDir, rel)); err != nil {
		return fmt.Errorf("memory: archive delete: %w", err)
	}
	if _, err := s.commitPushLocked(ctx, sp, "archive: удалена сессия "+sessionID, rel); err != nil {
		return err
	}
	return nil
}

// renderArchive собирает session.md: frontmatter с метой + тело с recap'ом агента.
func renderArchive(sess ArchivedSession) []byte {
	head, _ := yaml.Marshal(archiveFrontmatter{
		ID: sess.ID, Name: sess.Name, AgentType: sess.AgentType, Kind: sess.Kind,
		Created: sess.Created, Archived: sess.Archived,
	})
	var b bytes.Buffer
	b.Write(fmDelim)
	b.Write(head)
	b.WriteString("---\n\n")
	b.WriteString(strings.TrimRight(sess.Summary, "\n"))
	b.WriteByte('\n')
	return b.Bytes()
}

// parseArchive разбирает session.md обратно в ArchivedSession. false — файл без валидного
// frontmatter (посторонний markdown в каталоге).
func parseArchive(data []byte) (ArchivedSession, bool) {
	if !bytes.HasPrefix(data, fmDelim) {
		return ArchivedSession{}, false
	}
	rest := data[len(fmDelim):]
	end := bytes.Index(rest, fmDelim)
	if end < 0 {
		return ArchivedSession{}, false
	}
	var fm archiveFrontmatter
	if err := yaml.Unmarshal(rest[:end], &fm); err != nil || fm.ID == "" {
		return ArchivedSession{}, false
	}
	return ArchivedSession{
		ID:        fm.ID,
		Name:      fm.Name,
		AgentType: fm.AgentType,
		Kind:      fm.Kind,
		Summary:   strings.TrimSpace(string(rest[end+len(fmDelim):])),
		Created:   fm.Created,
		Archived:  fm.Archived,
	}, true
}

// ensureArchiveJSON — служебная проверка, что лента действительно JSON-массив. Пишем в архив
// только валидный JSON: иначе readonly-просмотр упадёт уже на чтении.
func ensureArchiveJSON(messages []byte) error {
	if len(messages) == 0 {
		return nil
	}
	var probe []json.RawMessage
	if err := json.Unmarshal(messages, &probe); err != nil {
		return fmt.Errorf("memory: archive messages: не JSON-массив: %w", err)
	}
	return nil
}
