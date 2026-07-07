package eventlog

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"
)

func ev(s string) json.RawMessage { return json.RawMessage(`"` + s + `"`) }

// TestAppendReadFrom проверяет монотонный seq и адресную докачку по offset.
func TestAppendReadFrom(t *testing.T) {
	l, err := Open("")
	if err != nil {
		t.Fatal(err)
	}
	for i, s := range []string{"a", "b", "c"} {
		seq, err := l.Append(ev(s))
		if err != nil || seq != int64(i+1) {
			t.Fatalf("append %s: seq=%d err=%v", s, seq, err)
		}
	}
	if l.LastSeq() != 3 {
		t.Fatalf("lastSeq=%d", l.LastSeq())
	}
	// докачка «после seq 1» → b, c
	got := l.ReadFrom(1)
	if len(got) != 2 || got[0].Seq != 2 || string(got[1].Data) != `"c"` {
		t.Fatalf("ReadFrom(1) = %+v", got)
	}
	if len(l.ReadFrom(3)) != 0 {
		t.Fatal("ReadFrom(3) должно быть пусто")
	}
}

// TestPersistReopen проверяет durability: события переживают закрытие/переоткрытие, seq продолжается.
func TestPersistReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "events.jsonl")
	l, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	l.Append(ev("one"))
	l.Append(ev("two"))
	l.Close()

	l2, err := Open(path) // переоткрытие (как демон на старте после смерти контейнера)
	if err != nil {
		t.Fatal(err)
	}
	if l2.LastSeq() != 2 {
		t.Fatalf("после reopen lastSeq=%d, want 2", l2.LastSeq())
	}
	seq, _ := l2.Append(ev("three")) // seq продолжается, не сбрасывается
	if seq != 3 {
		t.Fatalf("после reopen append seq=%d, want 3", seq)
	}
	if got := l2.ReadFrom(0); len(got) != 3 || string(got[0].Data) != `"one"` {
		t.Fatalf("reopen ReadFrom(0) = %+v", got)
	}
}

// TestFollow проверяет live-tail без потери событий при append во время ожидания.
func TestFollow(t *testing.T) {
	l, _ := Open("")
	l.Append(ev("hist")) // существующее событие — Follow должен его отдать
	done := make(chan struct{})
	got := make(chan string, 8)
	go func() {
		_ = l.Follow(done, 0, func(e Entry) error {
			got <- string(e.Data)
			return nil
		})
	}()
	// первое (историческое)
	if s := <-got; s != `"hist"` {
		t.Fatalf("first=%s", s)
	}
	// live-события после старта Follow
	l.Append(ev("live1"))
	l.Append(ev("live2"))
	for _, want := range []string{`"live1"`, `"live2"`} {
		select {
		case s := <-got:
			if s != want {
				t.Fatalf("got %s want %s", s, want)
			}
		case <-time.After(time.Second):
			t.Fatalf("timeout ждём %s", want)
		}
	}
	close(done)
}
