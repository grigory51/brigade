package memory

import "testing"

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
