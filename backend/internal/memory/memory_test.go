package memory

import "testing"

// TestRoundTrip проверяет, что render→parse сохраняет поля заметки, а normalize
// заполняет тип/дату/id и slug ведёт себя предсказуемо.
func TestRoundTrip(t *testing.T) {
	in := normalize(Note{Title: "Graph vs Kanban!", Type: "IDEA", Tags: []string{"brigade", "ui"}, Session: "abc", Body: "тело\nзаметки"})
	if in.Type != "idea" {
		t.Fatalf("type not normalized: %q", in.Type)
	}
	if in.Layer != layerSemantic {
		t.Fatalf("layer not defaulted to semantic: %q", in.Layer)
	}
	if in.ID == "" || in.Created == "" || in.Updated == "" {
		t.Fatalf("normalize left empty fields: %+v", in)
	}
	if in.ID[len(in.ID)-len("graph-vs-kanban"):] != "graph-vs-kanban" {
		t.Fatalf("unexpected id slug: %q", in.ID)
	}
	if notePath(in) != "ideas/"+in.ID+".md" {
		t.Fatalf("semantic path: %q", notePath(in))
	}

	out, ok := parseNote(renderNote(in))
	if !ok {
		t.Fatal("parseNote failed on rendered note")
	}
	if out.ID != in.ID || out.Title != in.Title || out.Type != in.Type ||
		out.Layer != in.Layer || out.Session != in.Session || out.Body != in.Body || len(out.Tags) != 2 {
		t.Fatalf("round-trip mismatch:\n in=%+v\nout=%+v", in, out)
	}

	// Эпизодический слой → каталог sessions/.
	ep := normalize(Note{Title: "Session recap", Layer: "EPISODIC", Body: "что сделано"})
	if ep.Layer != layerEpisodic {
		t.Fatalf("episodic layer not normalized: %q", ep.Layer)
	}
	if notePath(ep) != "sessions/"+ep.ID+".md" {
		t.Fatalf("episodic path: %q", notePath(ep))
	}
	if out2, ok := parseNote(renderNote(ep)); !ok || out2.Layer != layerEpisodic {
		t.Fatalf("episodic round-trip: ok=%v layer=%q", ok, out2.Layer)
	}

	if !matches(out, "kanban") || matches(out, "нетакого") {
		t.Fatal("matches broken")
	}
	if _, ok := parseNote([]byte("no frontmatter here")); ok {
		t.Fatal("parseNote accepted non-note")
	}
}
