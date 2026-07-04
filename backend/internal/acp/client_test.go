package acp

import (
	"testing"
	"time"

	"github.com/grigory51/brigade/backend/internal/agui"
)

// usageEvent конструирует CUSTOM usage-событие для проверки emit/Bind.
func usageEvent(used, size int) agui.Event {
	return agui.Event{Type: agui.EventCustom, Name: agui.CustomUsageName, Value: &agui.Usage{Used: used, Size: size}}
}

// commandsEvent конструирует CUSTOM available_commands-событие.
func commandsEvent(names ...string) agui.Event {
	cmds := make([]agui.AvailableCommand, len(names))
	for i, n := range names {
		cmds[i] = agui.AvailableCommand{Name: n}
	}
	return agui.Event{Type: agui.EventCustom, Name: agui.CustomCommandsName, Value: agui.AvailableCommands{Commands: cmds}}
}

// TestEmitClassifies проверяет, что emit раскладывает события по назначению: usage и
// commands обновляют last*-снимки и не копятся в history, а обычные события идут в history.
func TestEmitClassifies(t *testing.T) {
	c := &Client{}

	c.emit(usageEvent(10, 100))
	c.emit(commandsEvent("plan"))
	c.emit(agui.Event{Type: agui.EventTextMessageStart, MessageID: "m1", Role: "assistant"})

	if len(c.history) != 1 {
		t.Fatalf("в history %d событий, want 1 (usage/commands не должны копиться)", len(c.history))
	}
	if c.history[0].Type != agui.EventTextMessageStart {
		t.Errorf("history[0].Type = %s, want %s", c.history[0].Type, agui.EventTextMessageStart)
	}
	if c.lastUsage == nil || c.lastUsage.Value.(*agui.Usage).Used != 10 {
		t.Errorf("lastUsage не обновлён: %+v", c.lastUsage)
	}
	if c.lastCommands == nil {
		t.Fatal("lastCommands = nil, want заполненный")
	}
	if cmds := c.lastCommands.Value.(agui.AvailableCommands).Commands; len(cmds) != 1 || cmds[0].Name != "plan" {
		t.Errorf("lastCommands = %+v, want [plan]", cmds)
	}
}

// TestEmitLastWins проверяет, что повторные usage/commands заменяют снимок последним
// значением, а не накапливаются.
func TestEmitLastWins(t *testing.T) {
	c := &Client{}
	c.emit(usageEvent(10, 100))
	c.emit(usageEvent(20, 100))
	c.emit(commandsEvent("a"))
	c.emit(commandsEvent("b", "c"))

	if u := c.lastUsage.Value.(*agui.Usage); u.Used != 20 {
		t.Errorf("lastUsage.Used = %d, want 20 (последнее значение)", u.Used)
	}
	if cmds := c.lastCommands.Value.(agui.AvailableCommands).Commands; len(cmds) != 2 {
		t.Errorf("lastCommands = %+v, want 2 команды", cmds)
	}
}

// TestEmitHistoryCap проверяет соблюдение historyCap: при переполнении отбрасывается
// голова, хвост сохраняется.
func TestEmitHistoryCap(t *testing.T) {
	c := &Client{}
	total := historyCap + 10
	for i := 0; i < total; i++ {
		c.emit(agui.Event{Type: agui.EventTextMessageContent, MessageID: "m", Delta: string(rune('a' + i%26))})
	}
	if len(c.history) != historyCap {
		t.Fatalf("len(history) = %d, want %d (кап)", len(c.history), historyCap)
	}
	// Первым в срезе должно остаться событие с индексом total-historyCap: голова отброшена.
	wantFirst := string(rune('a' + (total-historyCap)%26))
	if c.history[0].Delta != wantFirst {
		t.Errorf("history[0].Delta = %q, want %q (голова отброшена)", c.history[0].Delta, wantFirst)
	}
}

// TestEmitNilSink проверяет, что emit без привязанного sink не паникует и всё равно копит
// событие в history.
func TestEmitNilSink(t *testing.T) {
	c := &Client{}
	c.emit(agui.Event{Type: agui.EventTextMessageStart, MessageID: "m1"})
	if len(c.history) != 1 {
		t.Errorf("len(history) = %d, want 1", len(c.history))
	}
}

// TestEmitDeliversToSink проверяет, что привязанный sink получает событие.
func TestEmitDeliversToSink(t *testing.T) {
	c := &Client{}
	var got []agui.Event
	c.sink = func(e agui.Event) error { got = append(got, e); return nil }

	evt := agui.Event{Type: agui.EventTextMessageContent, MessageID: "m1", Delta: "x"}
	c.emit(evt)

	if len(got) != 1 || got[0].Delta != "x" {
		t.Errorf("sink получил %+v, want [{delta:x}]", got)
	}
}

// TestStatus проверяет детект «агент генерирует» и монотонный seq: свежая
// содержательная активность (или живой Prompt) → generating=true, устаревшая → false;
// seq растёт по числу событий ленты.
func TestStatus(t *testing.T) {
	c := &Client{}
	if gen, seq := c.Status(); gen || seq != 0 {
		t.Fatalf("пустой клиент: generating=%v seq=%d, want false 0", gen, seq)
	}

	// Содержательное событие фонового turn'а: активность свежая → generating.
	c.emit(agui.Event{Type: agui.EventTextMessageContent, MessageID: "m1", Delta: "x"})
	if gen, seq := c.Status(); !gen || seq != 1 {
		t.Fatalf("после активности: generating=%v seq=%d, want true 1", gen, seq)
	}

	// Активность за пределами окна → idle, но seq сохраняется.
	c.lastActivityAt = time.Now().Add(-backgroundIdleWindow - time.Second)
	if gen, seq := c.Status(); gen || seq != 1 {
		t.Fatalf("после окна: generating=%v seq=%d, want false 1", gen, seq)
	}

	// Живой Prompt держит generating независимо от давности активности.
	c.promptActive = true
	if gen, _ := c.Status(); !gen {
		t.Fatal("promptActive: generating=false, want true")
	}
}

// TestRecordUserMessageAndMessages проверяет, что recordUserMessage кладёт тройку
// user-сообщения в history, а Messages агрегирует её в одно Message с role=user.
func TestRecordUserMessageAndMessages(t *testing.T) {
	c := &Client{}
	c.recordUserMessage("привет")

	if len(c.history) != 3 {
		t.Fatalf("в history %d событий, want 3 (START/CONTENT/END)", len(c.history))
	}
	msgs := c.Messages()
	if len(msgs) != 1 {
		t.Fatalf("Messages вернул %d сообщений, want 1", len(msgs))
	}
	if msgs[0].Role != "user" || msgs[0].Content != "привет" {
		t.Errorf("Message = %+v, want {role:user content:привет}", msgs[0])
	}
}

// TestMessagesOrdering проверяет, что чередование user/assistant сохраняет порядок и
// роли, а ассистентские чанки склеиваются по messageId.
func TestMessagesOrdering(t *testing.T) {
	c := &Client{}
	c.recordUserMessage("q1")
	// Ассистентский ответ двумя чанками с общим id.
	c.emit(agui.Event{Type: agui.EventTextMessageStart, MessageID: "a1", Role: "assistant"})
	c.emit(agui.Event{Type: agui.EventTextMessageContent, MessageID: "a1", Delta: "ans"})
	c.emit(agui.Event{Type: agui.EventTextMessageContent, MessageID: "a1", Delta: "wer"})
	c.emit(agui.Event{Type: agui.EventTextMessageEnd, MessageID: "a1"})
	c.recordUserMessage("q2")

	msgs := c.Messages()
	want := []Message{
		{ID: msgs[0].ID, Role: "user", Content: "q1"},
		{ID: "a1", Role: "assistant", Content: "answer"},
		{ID: msgs[2].ID, Role: "user", Content: "q2"},
	}
	if len(msgs) != len(want) {
		t.Fatalf("Messages вернул %d сообщений %+v, want %d", len(msgs), msgs, len(want))
	}
	for i := range want {
		if msgs[i].Role != want[i].Role || msgs[i].Content != want[i].Content {
			t.Errorf("msg[%d] = %+v, want role=%s content=%s", i, msgs[i], want[i].Role, want[i].Content)
		}
	}
}

// TestCommands проверяет, что Commands возвращает nil до первого available_commands и
// список после emit.
func TestCommands(t *testing.T) {
	c := &Client{}
	if got := c.Commands(); got != nil {
		t.Errorf("Commands до emit = %+v, want nil", got)
	}

	c.emit(commandsEvent("plan", "clear"))
	got := c.Commands()
	if len(got) != 2 || got[0].Name != "plan" || got[1].Name != "clear" {
		t.Errorf("Commands = %+v, want [plan clear]", got)
	}
}

// TestBindDoesNotReplayHistory проверяет, что Bind НЕ проигрывает history в sink, но
// доставляет снимки commands и usage (в порядке commands→usage).
func TestBindDoesNotReplayHistory(t *testing.T) {
	c := &Client{}
	// Накопим историю и снимки до Bind.
	c.emit(agui.Event{Type: agui.EventTextMessageStart, MessageID: "m1", Role: "assistant"})
	c.emit(agui.Event{Type: agui.EventTextMessageContent, MessageID: "m1", Delta: "hi"})
	c.emit(usageEvent(5, 50))
	c.emit(commandsEvent("plan"))

	var got []agui.Event
	sink := func(e agui.Event) error { got = append(got, e); return nil }
	unbind := c.Bind(sink, nil)
	defer unbind()

	// history не должна попасть в sink; должны прийти только commands и usage.
	if len(got) != 2 {
		t.Fatalf("sink получил %d событий %+v, want 2 (commands+usage)", len(got), shapes(got))
	}
	if got[0].Name != agui.CustomCommandsName {
		t.Errorf("got[0].Name = %q, want %q (commands первыми)", got[0].Name, agui.CustomCommandsName)
	}
	if got[1].Name != agui.CustomUsageName {
		t.Errorf("got[1].Name = %q, want %q (usage вторым)", got[1].Name, agui.CustomUsageName)
	}
}

// TestBindReopensOpenStreams проверяет, что при открытом текстовом потоке Bind шлёт
// переоткрывающий TEXT_MESSAGE_START (role assistant), чтобы последующие CONTENT/END не
// прилетели в новый sink без START.
func TestBindReopensOpenStreams(t *testing.T) {
	c := &Client{}
	// Открываем текстовый поток напрямую (эмуляция незакрытого turn'а).
	c.stream.textID = "m1"

	var got []agui.Event
	sink := func(e agui.Event) error { got = append(got, e); return nil }
	unbind := c.Bind(sink, nil)
	defer unbind()

	if len(got) != 1 {
		t.Fatalf("sink получил %d событий %+v, want 1 (переоткрытие потока)", len(got), shapes(got))
	}
	if got[0].Type != agui.EventTextMessageStart || got[0].MessageID != "m1" || got[0].Role != "assistant" {
		t.Errorf("got[0] = %+v, want START(m1, assistant)", shape(got[0]))
	}
}

// TestBindReopensOpenThought проверяет переоткрытие незакрытого блока размышлений:
// REASONING_START + REASONING_MESSAGE_START.
func TestBindReopensOpenThought(t *testing.T) {
	c := &Client{}
	c.stream.thoughtID = "t1"

	var got []agui.Event
	sink := func(e agui.Event) error { got = append(got, e); return nil }
	unbind := c.Bind(sink, nil)
	defer unbind()

	assertShapes(t, got, []eventShape{
		{Type: agui.EventReasoningStart, MessageID: "t1"},
		{Type: agui.EventReasoningMessageStart, MessageID: "t1", Role: "reasoning"},
	})
}

// TestBindUnbindGeneration проверяет, что unbind снимает только свою привязку: после
// второго Bind unbind первого поколения не должен затирать актуальную привязку, и emit
// приходит во второй sink.
func TestBindUnbindGeneration(t *testing.T) {
	c := &Client{}

	var got1, got2 []agui.Event
	sink1 := func(e agui.Event) error { got1 = append(got1, e); return nil }
	sink2 := func(e agui.Event) error { got2 = append(got2, e); return nil }

	unbind1 := c.Bind(sink1, nil)
	c.Bind(sink2, nil) // второе поколение перехватывает привязку
	unbind1()          // unbind старого поколения не должен снять новую привязку

	c.emit(agui.Event{Type: agui.EventTextMessageContent, MessageID: "m", Delta: "x"})

	if len(got1) != 0 {
		t.Errorf("первый sink получил %+v, want пусто (перепривязан)", got1)
	}
	// Второй sink должен получить событие (привязка жива после unbind первого).
	if len(got2) != 1 || got2[0].Delta != "x" {
		t.Errorf("второй sink получил %+v, want [{delta:x}]", got2)
	}
}
