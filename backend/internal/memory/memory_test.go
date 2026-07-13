package memory

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/grigory51/brigade/backend/internal/store"
)

// fakeSettings — источник настроек памяти для тестов (только remote, без SSH-ключа).
type fakeSettings struct{ remote string }

func (f fakeSettings) GetUserSettings(context.Context, string) (store.UserSettings, error) {
	return store.UserSettings{MemoryRemote: f.remote}, nil
}

// TestSelfHeal проверяет, что битый локальный клон (повреждённый HEAD после сорванного
// git-процесса) переклонируется, и запись после этого проходит.
func TestSelfHeal(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git не доступен")
	}
	base := t.TempDir()
	bare := filepath.Join(t.TempDir(), "remote.git")
	if out, err := exec.Command("git", "init", "--bare", "-b", "main", bare).CombinedOutput(); err != nil {
		t.Fatalf("init bare: %v: %s", err, out)
	}

	svc := NewService(base, fakeSettings{remote: bare}, nil)
	ctx := context.Background()
	const userID = "u1"

	// Первая заметка: clone (пустой bare) + commit + push.
	if _, _, err := svc.Create(ctx, userID, Note{Title: "one", Body: "first"}); err != nil {
		t.Fatalf("create 1: %v", err)
	}

	// Ломаем HEAD локального клона (симуляция сорванного git-процесса).
	repoDir := filepath.Join(base, userID, "repo")
	if err := os.WriteFile(filepath.Join(repoDir, ".git", "HEAD"), []byte("broken\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if svc.repoHealthy(ctx, space{repoDir: repoDir}) {
		t.Fatal("repoHealthy должен быть false при битом HEAD")
	}

	// Вторая заметка: self-heal (переклонирование) и успешная запись.
	if _, _, err := svc.Create(ctx, userID, Note{Title: "two", Body: "second"}); err != nil {
		t.Fatalf("create 2 после порчи HEAD: %v", err)
	}
	notes, err := svc.List(ctx, userID, "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(notes) != 2 {
		t.Fatalf("после self-heal ждём 2 заметки, получили %d", len(notes))
	}
}

// TestRoundTrip проверяет, что render→parse сохраняет поля заметки, а normalize
// заполняет тип/дату/id/тему и slug ведёт себя предсказуемо.
func TestRoundTrip(t *testing.T) {
	in := normalize(Note{Title: "Graph vs Kanban!", Type: "IDEA", Tags: []string{"brigade", "ui"}, Session: "abc", Sub: "UI", Body: "тело\nзаметки"})
	if in.Type != "idea" {
		t.Fatalf("type not normalized: %q", in.Type)
	}
	if in.Layer != layerSemantic {
		t.Fatalf("layer not defaulted to semantic: %q", in.Layer)
	}
	if in.TopicID != generalTopicID {
		t.Fatalf("topic not defaulted to general: %q", in.TopicID)
	}
	if in.ID == "" || in.Created == "" || in.Updated == "" {
		t.Fatalf("normalize left empty fields: %+v", in)
	}
	if in.ID[len(in.ID)-len("graph-vs-kanban"):] != "graph-vs-kanban" {
		t.Fatalf("unexpected id slug: %q", in.ID)
	}
	// Новые заметки живут в теме: topics/<topic>/<id>.md.
	if notePath(in) != "topics/general/"+in.ID+".md" {
		t.Fatalf("note path: %q", notePath(in))
	}

	out, ok := parseNote(renderNote(in))
	if !ok {
		t.Fatal("parseNote failed on rendered note")
	}
	if out.ID != in.ID || out.Title != in.Title || out.Type != in.Type ||
		out.Layer != in.Layer || out.Session != in.Session || out.Sub != in.Sub ||
		out.Body != in.Body || len(out.Tags) != 2 {
		t.Fatalf("round-trip mismatch:\n in=%+v\nout=%+v", in, out)
	}

	if !matches(out, "kanban") || matches(out, "нетакого") {
		t.Fatal("matches broken")
	}
	if _, ok := parseNote([]byte("no frontmatter here")); ok {
		t.Fatal("parseNote accepted non-note")
	}
}

// TestTopicModel покрывает чистую логику тем: round-trip меты, резолв темы из пути,
// агрегацию (legacy → «Общее» + реальная тема, производные, слияние подтем) и slug.
func TestTopicModel(t *testing.T) {
	// Round-trip _topic.md (frontmatter + тело=synthesis).
	tp := Topic{ID: "arch", Name: "Архитектура", Color: "#c96442", Initial: "А",
		Subs: []string{"Общее", "API"}, Synthesis: "О чём эта тема\n\nдетали", Created: "2026-01-01", Updated: "2026-02-02"}
	got, ok := parseTopic(renderTopic(tp))
	if !ok || got.ID != tp.ID || got.Name != tp.Name || got.Color != tp.Color ||
		got.Synthesis != tp.Synthesis || len(got.Subs) != 2 {
		t.Fatalf("topic round-trip mismatch: ok=%v got=%+v", ok, got)
	}

	// TopicID из пути файла: topics/<id>/ → id; legacy → general.
	if topicIDFromRel("topics/arch/2026-01-01-x.md") != "arch" {
		t.Fatal("topicIDFromRel: topics path")
	}
	if topicIDFromRel("ideas/2026-01-01-x.md") != generalTopicID {
		t.Fatal("topicIDFromRel: legacy → general")
	}

	// Агрегация: реальная тема (мета) + legacy-заметки без темы.
	metas := []Topic{{ID: "arch", Name: "Архитектура", Subs: []string{"Общее"}}}
	files := []noteFile{
		{Note: Note{ID: "n1", TopicID: "arch", Sub: "API", Session: "s1", Updated: "2026-03-01"}, Rel: "topics/arch/n1.md"},
		{Note: Note{ID: "n2", TopicID: "arch", Session: "s1", Updated: "2026-03-05"}, Rel: "topics/arch/n2.md"},
		{Note: Note{ID: "n3", TopicID: generalTopicID, Session: "s2", Updated: "2026-02-01"}, Rel: "ideas/n3.md"},
	}
	topics := assembleTopics(metas, files)
	byID := map[string]Topic{}
	for _, x := range topics {
		byID[x.ID] = x
	}
	arch, ok := byID["arch"]
	if !ok || arch.NoteCount != 2 || arch.ChatCount != 1 || arch.Updated != "2026-03-05" {
		t.Fatalf("arch derived wrong: %+v", arch)
	}
	// Подтема API из заметки должна влиться к «Общее» из меты.
	if len(arch.Subs) != 2 || arch.Subs[0] != "Общее" || arch.Subs[1] != "API" {
		t.Fatalf("subs merge wrong: %+v", arch.Subs)
	}
	gen, ok := byID[generalTopicID]
	if !ok || gen.Name != generalTopicName || gen.NoteCount != 1 {
		t.Fatalf("virtual general wrong: %+v", gen)
	}

	// Slug сохраняет кириллицу (иначе имена тем схлопывались бы в «note»).
	if topicSlug("Личная память!") != "личная-память" {
		t.Fatalf("topicSlug cyrillic: %q", topicSlug("Личная память!"))
	}
	taken := map[string]bool{"api": true}
	if uniqueTopicID("api", taken) != "api-2" {
		t.Fatal("uniqueTopicID suffix")
	}
}

// TestCreateNoteInTopic: заметка с ИМЕНЕМ темы уходит в эту тему (создаёт её при отсутствии),
// повторное имя переиспользует тему без дубля, пустое имя → «Общее». Регрессия: раньше агент не
// мог задать тему и всё падало в «Общее».
func TestCreateNoteInTopic(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git не доступен")
	}
	base := t.TempDir()
	bare := filepath.Join(t.TempDir(), "remote.git")
	if out, err := exec.Command("git", "init", "--bare", "-b", "main", bare).CombinedOutput(); err != nil {
		t.Fatalf("init bare: %v: %s", err, out)
	}
	svc := NewService(base, fakeSettings{remote: bare}, nil)
	ctx := context.Background()
	const userID = "u1"

	diyCount := func() int {
		topics, err := svc.ListTopics(ctx, userID, "")
		if err != nil {
			t.Fatalf("list topics: %v", err)
		}
		c := 0
		for _, tp := range topics {
			if strings.EqualFold(tp.Name, "DIY") {
				c++
			}
		}
		return c
	}

	n1, _, err := svc.CreateNoteInTopic(ctx, userID, "DIY", Note{Title: "аккумуляторы", Body: "b"})
	if err != nil {
		t.Fatalf("create in DIY: %v", err)
	}
	if n1.TopicID == generalTopicID || n1.TopicID == "" {
		t.Errorf("заметка ушла в general, а не в тему DIY: %q", n1.TopicID)
	}
	if c := diyCount(); c != 1 {
		t.Errorf("тем DIY после первой заметки = %d, want 1", c)
	}

	// Повторное имя — та же тема, без дубля.
	n2, _, err := svc.CreateNoteInTopic(ctx, userID, "DIY", Note{Title: "вторая"})
	if err != nil {
		t.Fatalf("create 2 in DIY: %v", err)
	}
	if n2.TopicID != n1.TopicID {
		t.Errorf("вторая заметка в другой теме: %q vs %q", n2.TopicID, n1.TopicID)
	}
	if c := diyCount(); c != 1 {
		t.Errorf("тем DIY после второй заметки = %d, want 1 (дубль темы)", c)
	}

	// Пустое имя темы → «Общее».
	n3, _, err := svc.CreateNoteInTopic(ctx, userID, "", Note{Title: "без темы"})
	if err != nil {
		t.Fatalf("create в общее: %v", err)
	}
	if n3.TopicID != generalTopicID {
		t.Errorf("пустая тема → %q, want %q", n3.TopicID, generalTopicID)
	}
}
