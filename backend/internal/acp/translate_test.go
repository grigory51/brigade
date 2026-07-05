package acp

import (
	"strings"
	"testing"

	acpsdk "github.com/coder/acp-go-sdk"

	"github.com/grigory51/brigade/backend/internal/a2ui"
	"github.com/grigory51/brigade/backend/internal/agui"
)

// eventShape — усечённая форма AG-UI события для сравнения в table-тестах: сверяем тип и
// связующие поля (messageId/role/delta/toolCallId), а не всю структуру целиком.
type eventShape struct {
	Type       agui.EventType
	MessageID  string
	Role       string
	Delta      string
	ToolCallID string
}

func shape(e agui.Event) eventShape {
	return eventShape{
		Type:       e.Type,
		MessageID:  e.MessageID,
		Role:       e.Role,
		Delta:      e.Delta,
		ToolCallID: e.ToolCallID,
	}
}

func shapes(evts []agui.Event) []eventShape {
	out := make([]eventShape, len(evts))
	for i, e := range evts {
		out[i] = shape(e)
	}
	return out
}

func assertShapes(t *testing.T, got []agui.Event, want []eventShape) {
	t.Helper()
	gs := shapes(got)
	if len(gs) != len(want) {
		t.Fatalf("получено %d событий %+v, ожидалось %d %+v", len(gs), gs, len(want), want)
	}
	for i := range want {
		if gs[i] != want[i] {
			t.Errorf("событие[%d] = %+v, want %+v", i, gs[i], want[i])
		}
	}
}

// agentMessageChunk конструирует agent_message_chunk с явным messageId. Явный id важен:
// поток открывается лишь при непустом messageId, отличном от текущего (см. streamText).
func agentMessageChunk(id, text string) acpsdk.SessionUpdate {
	mid := id
	return acpsdk.SessionUpdate{AgentMessageChunk: &acpsdk.SessionUpdateAgentMessageChunk{
		MessageId: &mid,
		Content:   acpsdk.TextBlock(text),
	}}
}

// agentThoughtChunk конструирует agent_thought_chunk с явным messageId.
func agentThoughtChunk(id, text string) acpsdk.SessionUpdate {
	mid := id
	return acpsdk.SessionUpdate{AgentThoughtChunk: &acpsdk.SessionUpdateAgentThoughtChunk{
		MessageId: &mid,
		Content:   acpsdk.TextBlock(text),
	}}
}

// usageUpdate конструирует ACP usage_update (helper в SDK отсутствует).
func usageUpdate(used, size int, cost *acpsdk.Cost) acpsdk.SessionUpdate {
	return acpsdk.SessionUpdate{UsageUpdate: &acpsdk.SessionUsageUpdate{
		Used: used,
		Size: size,
		Cost: cost,
	}}
}

// commandsUpdate конструирует ACP available_commands_update (helper в SDK отсутствует).
func commandsUpdate(cmds ...acpsdk.AvailableCommand) acpsdk.SessionUpdate {
	return acpsdk.SessionUpdate{AvailableCommandsUpdate: &acpsdk.SessionAvailableCommandsUpdate{
		AvailableCommands: cmds,
	}}
}

// TestTranslateAgentMessageChunk проверяет обёртывание потокового текста ассистента:
// первый чанк даёт START+CONTENT, повтор того же id — только CONTENT, смена id —
// END старого сообщения перед START+CONTENT нового.
func TestTranslateAgentMessageChunk(t *testing.T) {
	c := &Client{}

	got := c.translateUpdate(agentMessageChunk("m1", "hello"))
	assertShapes(t, got, []eventShape{
		{Type: agui.EventTextMessageStart, MessageID: "m1", Role: "assistant"},
		{Type: agui.EventTextMessageContent, MessageID: "m1", Delta: "hello"},
	})

	// Второй чанк с тем же id — сообщение уже открыто, только CONTENT.
	got = c.translateUpdate(agentMessageChunk("m1", " world"))
	assertShapes(t, got, []eventShape{
		{Type: agui.EventTextMessageContent, MessageID: "m1", Delta: " world"},
	})

	// Смена messageId: закрываем предыдущее сообщение, открываем новое.
	got = c.translateUpdate(agentMessageChunk("m2", "second"))
	assertShapes(t, got, []eventShape{
		{Type: agui.EventTextMessageEnd, MessageID: "m1"},
		{Type: agui.EventTextMessageStart, MessageID: "m2", Role: "assistant"},
		{Type: agui.EventTextMessageContent, MessageID: "m2", Delta: "second"},
	})
}

// TestTranslateAgentThoughtChunk проверяет открытие блока размышлений и переключение с
// размышления на текст: чанк текста закрывает reasoning (MESSAGE_END+END) перед вставкой.
func TestTranslateAgentThoughtChunk(t *testing.T) {
	c := &Client{}

	got := c.translateUpdate(agentThoughtChunk("t1", "thinking"))
	assertShapes(t, got, []eventShape{
		{Type: agui.EventReasoningStart, MessageID: "t1"},
		{Type: agui.EventReasoningMessageStart, MessageID: "t1", Role: "reasoning"},
		{Type: agui.EventReasoningMessageContent, MessageID: "t1", Delta: "thinking"},
	})

	// Текстовый чанк закрывает открытый reasoning перед своим START.
	got = c.translateUpdate(agentMessageChunk("m1", "answer"))
	assertShapes(t, got, []eventShape{
		{Type: agui.EventReasoningMessageEnd, MessageID: "t1"},
		{Type: agui.EventReasoningEnd, MessageID: "t1"},
		{Type: agui.EventTextMessageStart, MessageID: "m1", Role: "assistant"},
		{Type: agui.EventTextMessageContent, MessageID: "m1", Delta: "answer"},
	})
}

// TestTranslateUserMessageChunk проверяет трансляцию реплики пользователя: при
// promptActive=false — самодостаточная тройка role=user; при promptActive=true сообщение
// подавляется (эмитятся только закрытия открытых потоков, если они были).
func TestTranslateUserMessageChunk(t *testing.T) {
	t.Run("promptActive=false", func(t *testing.T) {
		c := &Client{}
		mid := "u1"
		got := c.translateUpdate(acpsdk.SessionUpdate{UserMessageChunk: &acpsdk.SessionUpdateUserMessageChunk{
			MessageId: &mid,
			Content:   acpsdk.TextBlock("hi"),
		}})
		assertShapes(t, got, []eventShape{
			{Type: agui.EventTextMessageStart, MessageID: "u1", Role: "user"},
			{Type: agui.EventTextMessageContent, MessageID: "u1", Delta: "hi"},
			{Type: agui.EventTextMessageEnd, MessageID: "u1"},
		})
	})

	t.Run("promptActive=true подавляет сообщение", func(t *testing.T) {
		c := &Client{promptActive: true}
		got := c.translateUpdate(acpsdk.UpdateUserMessageText("hi"))
		assertShapes(t, got, nil)
	})

	t.Run("синтетическое уведомление → системная карточка", func(t *testing.T) {
		// user_message_chunk с маркером инжекции харнесса (wake-up о фоновой задаче)
		// транслируется role=system с выжимкой из <summary> — и живьём, и в реплее
		// session/load (проверка безусловна, фаза не важна).
		c := &Client{}
		mid := "n1"
		got := c.translateUpdate(acpsdk.SessionUpdate{UserMessageChunk: &acpsdk.SessionUpdateUserMessageChunk{
			MessageId: &mid,
			Content:   acpsdk.TextBlock("<task-notification><task-id>x</task-id><summary>Задача завершена</summary></task-notification>"),
		}})
		assertShapes(t, got, []eventShape{
			{Type: agui.EventTextMessageStart, MessageID: "n1", Role: "system"},
			{Type: agui.EventTextMessageContent, MessageID: "n1", Delta: "Задача завершена"},
			{Type: agui.EventTextMessageEnd, MessageID: "n1"},
		})
	})

	t.Run("реплика с XML-префиксом остаётся репликой пользователя", func(t *testing.T) {
		// Точные маркеры не матчат произвольный "<": пользовательский текст со
		// вставленным HTML/кодом не должен превращаться в системную карточку.
		c := &Client{}
		mid := "u2"
		got := c.translateUpdate(acpsdk.SessionUpdate{UserMessageChunk: &acpsdk.SessionUpdateUserMessageChunk{
			MessageId: &mid,
			Content:   acpsdk.TextBlock(`<div class="x"> не центрируется, почему?`),
		}})
		assertShapes(t, got, []eventShape{
			{Type: agui.EventTextMessageStart, MessageID: "u2", Role: "user"},
			{Type: agui.EventTextMessageContent, MessageID: "u2", Delta: `<div class="x"> не центрируется, почему?`},
			{Type: agui.EventTextMessageEnd, MessageID: "u2"},
		})
	})

	t.Run("promptActive=true закрывает открытый текстовый поток", func(t *testing.T) {
		c := &Client{promptActive: true}
		// Открываем текстовое сообщение ассистента.
		c.translateUpdate(agentMessageChunk("m1", "draft"))
		// User-сообщение подавлено, но открытый поток закрывается.
		got := c.translateUpdate(acpsdk.UpdateUserMessageText("hi"))
		assertShapes(t, got, []eventShape{
			{Type: agui.EventTextMessageEnd, MessageID: "m1"},
		})
	})
}

// TestSystemNotificationSummary проверяет выжимку из синтетического уведомления:
// содержимое <summary>; без него — текст без тегов с усечением; пустой ввод — заглушка.
func TestSystemNotificationSummary(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"summary", "<task-notification><summary>Готово: отчёт собран</summary></task-notification>", "Готово: отчёт собран"},
		{"вложенные теги внутри summary вычищаются", "<task-notification><summary>Итог <b>жирный</b></summary></task-notification>", "Итог жирный"},
		{"без summary — теги вычищены", "<x>привет</x> <y>мир</y>", "привет мир"},
		{"пусто", "<a></a>", "Системное уведомление"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := systemNotificationSummary(tc.in); got != tc.want {
				t.Errorf("systemNotificationSummary(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
	t.Run("усечение длинного текста", func(t *testing.T) {
		long := strings.Repeat("а", 400)
		got := systemNotificationSummary(long)
		if r := []rune(got); len(r) != 301 || r[300] != '…' {
			t.Errorf("len = %d, want 301 с многоточием на конце", len(r))
		}
	})
}

// TestTranslateAvailableCommands проверяет маппинг списка slash-команд в одно CUSTOM с
// корректными Name/Description/Hint (из Input.Unstructured).
func TestTranslateAvailableCommands(t *testing.T) {
	c := &Client{}
	got := c.translateUpdate(commandsUpdate(
		acpsdk.AvailableCommand{
			Name:        "plan",
			Description: "create a plan",
			Input: &acpsdk.AvailableCommandInput{
				Unstructured: &acpsdk.UnstructuredCommandInput{Hint: "topic"},
			},
		},
		acpsdk.AvailableCommand{Name: "clear", Description: "clear context"},
	))

	if len(got) != 1 {
		t.Fatalf("получено %d событий, want 1", len(got))
	}
	evt := got[0]
	if evt.Type != agui.EventCustom || evt.Name != agui.CustomCommandsName {
		t.Fatalf("событие = {%s %s}, want {CUSTOM %s}", evt.Type, evt.Name, agui.CustomCommandsName)
	}
	cmds, ok := evt.Value.(agui.AvailableCommands)
	if !ok {
		t.Fatalf("Value имеет тип %T, want agui.AvailableCommands", evt.Value)
	}
	want := []agui.AvailableCommand{
		{Name: "plan", Description: "create a plan", Hint: "topic"},
		{Name: "clear", Description: "clear context", Hint: ""},
	}
	if len(cmds.Commands) != len(want) {
		t.Fatalf("получено %d команд, want %d", len(cmds.Commands), len(want))
	}
	for i := range want {
		if cmds.Commands[i] != want[i] {
			t.Errorf("команда[%d] = %+v, want %+v", i, cmds.Commands[i], want[i])
		}
	}
}

// TestTranslateUsageUpdate проверяет трансляцию usage_update в CUSTOM usage с
// Used/Size/Cost.
func TestTranslateUsageUpdate(t *testing.T) {
	c := &Client{}
	got := c.translateUpdate(usageUpdate(120, 200000, &acpsdk.Cost{Amount: 0.42, Currency: "USD"}))

	if len(got) != 1 {
		t.Fatalf("получено %d событий, want 1", len(got))
	}
	evt := got[0]
	if evt.Type != agui.EventCustom || evt.Name != agui.CustomUsageName {
		t.Fatalf("событие = {%s %s}, want {CUSTOM %s}", evt.Type, evt.Name, agui.CustomUsageName)
	}
	usage, ok := evt.Value.(*agui.Usage)
	if !ok {
		t.Fatalf("Value имеет тип %T, want *agui.Usage", evt.Value)
	}
	if usage.Used != 120 || usage.Size != 200000 {
		t.Errorf("usage = {used:%d size:%d}, want {120 200000}", usage.Used, usage.Size)
	}
	if usage.Cost == nil {
		t.Fatal("usage.Cost = nil, want заполненный")
	}
	if usage.Cost.Amount != 0.42 || usage.Cost.Currency != "USD" {
		t.Errorf("cost = {%v %s}, want {0.42 USD}", usage.Cost.Amount, usage.Cost.Currency)
	}
}

// TestTranslateUsageUpdateNoCost проверяет, что при отсутствии Cost поле не заполняется.
func TestTranslateUsageUpdateNoCost(t *testing.T) {
	c := &Client{}
	got := c.translateUpdate(usageUpdate(10, 100, nil))
	if len(got) != 1 {
		t.Fatalf("получено %d событий, want 1", len(got))
	}
	usage, ok := got[0].Value.(*agui.Usage)
	if !ok {
		t.Fatalf("Value имеет тип %T, want *agui.Usage", got[0].Value)
	}
	if usage.Cost != nil {
		t.Errorf("usage.Cost = %+v, want nil", usage.Cost)
	}
}

// TestTranslateToolCall проверяет tool_call: перед стартом закрываются открытые потоки
// (finishStreams), затем TOOL_CALL_START. Аргументы (RawInput) эмитятся не на старте, а
// одним TOOL_CALL_ARGS при закрытии вызова — см. toolCallState.argsJSON.
func TestTranslateToolCall(t *testing.T) {
	c := &Client{}
	// Откроем текстовый поток, чтобы убедиться, что он закрывается перед tool call.
	c.translateUpdate(agentMessageChunk("m1", "before tool"))

	got := c.translateUpdate(acpsdk.StartToolCall(
		"tc-1", "Read file",
		acpsdk.WithStartRawInput(map[string]any{"path": "/tmp/x"}),
	))
	assertShapes(t, got, []eventShape{
		{Type: agui.EventTextMessageEnd, MessageID: "m1"},
		{Type: agui.EventToolCallStart, ToolCallID: "tc-1"},
	})
	// TOOL_CALL_START несёт имя инструмента в отдельном поле.
	if got[1].ToolCallName != "Read file" {
		t.Errorf("ToolCallName = %q, want %q", got[1].ToolCallName, "Read file")
	}

	// Аргументы приходят при закрытии вызова: TOOL_CALL_ARGS с накопленным вводом, затем END.
	done := acpsdk.ToolCallStatusCompleted
	got = c.translateUpdate(acpsdk.UpdateToolCall("tc-1", acpsdk.WithUpdateStatus(done)))
	assertShapes(t, got, []eventShape{
		{Type: agui.EventToolCallArgs, ToolCallID: "tc-1", Delta: `{"path":"/tmp/x"}`},
		{Type: agui.EventToolCallEnd, ToolCallID: "tc-1"},
	})
}

// TestTranslateToolCallMCPInput проверяет доставку аргументов MCP-инструмента: начальный
// ToolCall.RawInput пуст, реальный ввод приходит ToolCallUpdate.RawInput (полным снимком)
// и эмитится одним TOOL_CALL_ARGS при закрытии. Без этого аргументы render_ui/show_choice
// (доставляемых MCP-сервером) терялись бы — карточка получала бы пустой {}.
func TestTranslateToolCallMCPInput(t *testing.T) {
	c := &Client{}
	got := c.translateUpdate(acpsdk.StartToolCall(
		"tc-m", "mcp__brigade__render_ui",
		acpsdk.WithStartRawInput(map[string]any{}),
	))
	assertShapes(t, got, []eventShape{
		{Type: agui.EventToolCallStart, ToolCallID: "tc-m"},
	})

	// Ввод приезжает обновлением — снимок целиком (не дельта).
	got = c.translateUpdate(acpsdk.UpdateToolCall(
		"tc-m",
		acpsdk.WithUpdateRawInput(map[string]any{"components": []any{map[string]any{"id": "root"}}}),
	))
	if len(got) != 0 {
		t.Fatalf("update с вводом: %d событий, want 0 (эмит при закрытии)", len(got))
	}

	done := acpsdk.ToolCallStatusCompleted
	got = c.translateUpdate(acpsdk.UpdateToolCall("tc-m", acpsdk.WithUpdateStatus(done)))
	assertShapes(t, got, []eventShape{
		{Type: agui.EventToolCallArgs, ToolCallID: "tc-m", Delta: `{"components":[{"id":"root"}]}`},
		{Type: agui.EventToolCallEnd, ToolCallID: "tc-m"},
	})
}

// TestTranslateToolCallNoInput проверяет tool_call без RawInput: только START (ARGS не
// эмитится).
func TestTranslateToolCallNoInput(t *testing.T) {
	c := &Client{}
	got := c.translateUpdate(acpsdk.StartToolCall("tc-2", "Think"))
	assertShapes(t, got, []eventShape{
		{Type: agui.EventToolCallStart, ToolCallID: "tc-2"},
	})
}

// TestTranslateToolCallUpdate проверяет жизненный цикл tool call'а: промежуточные
// обновления копят результат и НЕ эмитят событий (клиент требует ровно один END),
// терминальный статус даёт END + RESULT с последним содержательным выводом, повторный
// терминальный update для закрытого вызова событий не даёт.
func TestTranslateToolCallUpdate(t *testing.T) {
	c := &Client{}
	// START регистрирует вызов как открытый.
	_ = c.translateUpdate(acpsdk.StartToolCall("tc-1", "Edit"))

	// Промежуточный update (без терминального статуса) копится молча.
	got := c.translateUpdate(acpsdk.UpdateToolCall(
		"tc-1",
		acpsdk.WithUpdateRawOutput(map[string]any{"ok": true}),
	))
	if len(got) != 0 {
		t.Fatalf("промежуточный update: %d событий, want 0", len(got))
	}

	// Терминальный статус закрывает вызов: END + RESULT с накопленным выводом.
	done := acpsdk.ToolCallStatusCompleted
	got = c.translateUpdate(acpsdk.UpdateToolCall("tc-1", acpsdk.WithUpdateStatus(done)))
	assertShapes(t, got, []eventShape{
		{Type: agui.EventToolCallEnd, ToolCallID: "tc-1"},
		{Type: agui.EventToolCallResult, MessageID: "tc-1", Role: "tool", ToolCallID: "tc-1"},
	})
	if got[1].Content != `{"ok":true}` {
		t.Errorf("Content = %q, want %q", got[1].Content, `{"ok":true}`)
	}

	// Повторный терминальный update закрытого вызова — ничего.
	got = c.translateUpdate(acpsdk.UpdateToolCall("tc-1", acpsdk.WithUpdateStatus(done)))
	if len(got) != 0 {
		t.Fatalf("повторный терминальный update: %d событий, want 0", len(got))
	}
}

// TestTranslateToolCallUpdateStickyDiff проверяет «липкий diff»: статусная строка после
// diff-контента не затирает его — RESULT терминального закрытия несёт diff.
func TestTranslateToolCallUpdateStickyDiff(t *testing.T) {
	c := &Client{}
	_ = c.translateUpdate(acpsdk.StartToolCall("tc-d", "Edit"))

	_ = c.translateUpdate(acpsdk.UpdateToolCall(
		"tc-d",
		acpsdk.WithUpdateContent([]acpsdk.ToolCallContent{
			acpsdk.ToolDiffContent("/tmp/f.txt", "new", "old"),
		}),
	))
	done := acpsdk.ToolCallStatusCompleted
	got := c.translateUpdate(acpsdk.UpdateToolCall(
		"tc-d",
		acpsdk.WithUpdateRawOutput("file updated successfully"),
		acpsdk.WithUpdateStatus(done),
	))
	// Закрытие diff-вызова: END + RESULT + поставка A2UI-поверхности карточки.
	if len(got) != 3 {
		t.Fatalf("терминальный update: %d событий, want 3", len(got))
	}
	if got[1].Content == `"file updated successfully"` {
		t.Fatalf("статусная строка затёрла diff-результат")
	}
	for _, want := range []string{`"type":"diff"`, `"oldText":"old"`, `"newText":"new"`} {
		if !strings.Contains(got[1].Content, want) {
			t.Errorf("Content не содержит %s: %q", want, got[1].Content)
		}
	}

	if got[2].Type != agui.EventCustom || got[2].Name != agui.CustomA2UIName {
		t.Fatalf("событие[2] = {%s %s}, want {CUSTOM a2ui}", got[2].Type, got[2].Name)
	}
	payload, ok := got[2].Value.(map[string]any)
	if !ok {
		t.Fatalf("Value имеет тип %T, want map", got[2].Value)
	}
	msgs, ok := payload["messages"].([]a2ui.Message)
	if !ok || len(msgs) != 3 {
		t.Fatalf("messages = %T len %v, want []a2ui.Message из 3", payload["messages"], msgs)
	}
	if msgs[0].CreateSurface == nil || msgs[0].CreateSurface.SurfaceID != "tc-d" {
		t.Errorf("createSurface = %+v, want surfaceId tc-d", msgs[0].CreateSurface)
	}
	if msgs[2].UpdateDataModel == nil {
		t.Errorf("третье сообщение не updateDataModel: %+v", msgs[2])
	}
}

// TestCloseOpenToolCalls проверяет закрытие вызовов без терминального статуса в конце
// turn'а: END (+RESULT при накопленном выводе) один раз; повторное закрытие пусто.
func TestCloseOpenToolCalls(t *testing.T) {
	c := &Client{}
	_ = c.translateUpdate(acpsdk.StartToolCall("tc-3", "Terminal"))
	_ = c.translateUpdate(acpsdk.UpdateToolCall(
		"tc-3",
		acpsdk.WithUpdateRawOutput("partial output"),
	))

	got := c.closeOpenToolCalls()
	assertShapes(t, got, []eventShape{
		{Type: agui.EventToolCallEnd, ToolCallID: "tc-3"},
		{Type: agui.EventToolCallResult, MessageID: "tc-3", Role: "tool", ToolCallID: "tc-3"},
	})
	if got := c.closeOpenToolCalls(); len(got) != 0 {
		t.Fatalf("повторное закрытие: %d событий, want 0", len(got))
	}
}

// TestTranslateUnknownUpdate проверяет, что нетранслируемое обновление (смена режима)
// даёт пустой результат.
func TestTranslateUnknownUpdate(t *testing.T) {
	c := &Client{}
	got := c.translateUpdate(acpsdk.SessionUpdate{
		CurrentModeUpdate: &acpsdk.SessionCurrentModeUpdate{CurrentModeId: "default"},
	})
	if len(got) != 0 {
		t.Errorf("получено %d событий %+v, want 0", len(got), shapes(got))
	}
}

// TestInterruptMarkerCard проверяет карточку для маркера прерывания turn'а.
func TestInterruptMarkerCard(t *testing.T) {
	if !looksSyntheticNotification("[Request interrupted by user]") {
		t.Error("маркер прерывания не распознан")
	}
	if got := systemNotificationSummary("[Request interrupted by user]"); got != "Прервано пользователем" {
		t.Errorf("summary = %q, want «Прервано пользователем»", got)
	}
}
