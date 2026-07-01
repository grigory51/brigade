package acp

import (
	"testing"

	acpsdk "github.com/coder/acp-go-sdk"

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
// (finishStreams), затем TOOL_CALL_START и TOOL_CALL_ARGS при наличии RawInput.
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
		{Type: agui.EventToolCallArgs, ToolCallID: "tc-1", Delta: `{"path":"/tmp/x"}`},
	})
	// TOOL_CALL_START несёт имя инструмента в отдельном поле.
	if got[1].ToolCallName != "Read file" {
		t.Errorf("ToolCallName = %q, want %q", got[1].ToolCallName, "Read file")
	}
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

// TestTranslateToolCallUpdate проверяет tool_call_update с RawOutput: END + RESULT, где
// результат несёт messageId=toolCallId, role=tool и сериализованный вывод.
func TestTranslateToolCallUpdate(t *testing.T) {
	c := &Client{}
	got := c.translateUpdate(acpsdk.UpdateToolCall(
		"tc-1",
		acpsdk.WithUpdateRawOutput(map[string]any{"ok": true}),
	))
	assertShapes(t, got, []eventShape{
		{Type: agui.EventToolCallEnd, ToolCallID: "tc-1"},
		{Type: agui.EventToolCallResult, MessageID: "tc-1", Role: "tool", ToolCallID: "tc-1"},
	})
	if got[1].Content != `{"ok":true}` {
		t.Errorf("Content = %q, want %q", got[1].Content, `{"ok":true}`)
	}
}

// TestTranslateToolCallUpdateNoResult проверяет tool_call_update без вывода: только END.
func TestTranslateToolCallUpdateNoResult(t *testing.T) {
	c := &Client{}
	got := c.translateUpdate(acpsdk.UpdateToolCall("tc-3"))
	assertShapes(t, got, []eventShape{
		{Type: agui.EventToolCallEnd, ToolCallID: "tc-3"},
	})
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
