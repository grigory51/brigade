package acp

import (
	"encoding/json"

	acpsdk "github.com/coder/acp-go-sdk"

	"github.com/grigory51/brigade/backend/internal/agui"
)

// streamState отслеживает открытые потоковые сообщения агента (ответ и размышление),
// чтобы расставлять START/END вокруг чанков по смене messageId. ACP стримит текст и
// размышления чанками с общим messageId; смена messageId означает новое сообщение,
// отсутствие чанков данного вида к концу turn'а — что сообщение завершено (END
// эмитится при переключении вида потока и при finishTurn). Доступ — под Client.mu.
type streamState struct {
	// textID/thoughtID — messageId текущих открытых сообщений; пустая строка означает,
	// что соответствующий поток закрыт.
	textID    string
	thoughtID string
}

// translateUpdate преобразует одно ACP-обновление сессии (SessionUpdate) в ноль или
// более канонических событий AG-UI. Возврат среза, а не одного события, нужен потому,
// что часть ACP-обновлений разворачивается в несколько AG-UI-событий (START + CONTENT,
// или END предыдущего сообщения + START нового), а часть — игнорируется.
//
// Метод опирается на c.stream (под c.mu): он закрывает ранее открытое сообщение (END)
// при переходе к другому виду потока или к новому messageId. Завершение turn'а
// закрывает оставшиеся открытыми сообщения отдельно — см. finishStreams.
func (c *Client) translateUpdate(u acpsdk.SessionUpdate) []agui.Event {
	switch {
	case u.UserMessageChunk != nil:
		return c.emitUserMessage(deref(u.UserMessageChunk.MessageId), contentText(u.UserMessageChunk.Content))

	case u.AvailableCommandsUpdate != nil:
		return []agui.Event{availableCommandsEvent(u.AvailableCommandsUpdate)}

	case u.AgentMessageChunk != nil:
		id := deref(u.AgentMessageChunk.MessageId)
		return c.streamText(id, contentText(u.AgentMessageChunk.Content))

	case u.AgentThoughtChunk != nil:
		id := deref(u.AgentThoughtChunk.MessageId)
		return c.streamReasoning(id, contentText(u.AgentThoughtChunk.Content))

	case u.ToolCall != nil:
		tc := u.ToolCall
		evts := c.finishStreams()
		evts = append(evts, agui.Event{
			Type:         agui.EventToolCallStart,
			ToolCallID:   string(tc.ToolCallId),
			ToolCallName: tc.Title,
		})
		// Аргументы tool call отдаются строкой-фрагментом JSON (TOOL_CALL_ARGS.delta):
		// канон ждёт здесь сериализованный ввод, а не объект. RawInput — сырой JSON
		// инструмента; передаём его одним фрагментом.
		if tc.RawInput != nil {
			evts = append(evts, agui.Event{
				Type:       agui.EventToolCallArgs,
				ToolCallID: string(tc.ToolCallId),
				Delta:      rawJSON(tc.RawInput),
			})
		}
		return evts

	case u.ToolCallUpdate != nil:
		tu := u.ToolCallUpdate
		evt := agui.Event{
			Type:       agui.EventToolCallEnd,
			ToolCallID: string(tu.ToolCallId),
		}
		// Результат tool call (контент/сырой вывод) отдаётся отдельным событием строкой
		// (TOOL_CALL_RESULT.content), чтобы клиент мог обновить уже отрисованный компонент.
		// messageId результата равен toolCallId: ответ инструмента самостоятельного
		// messageId не имеет.
		if tu.RawOutput != nil || len(tu.Content) > 0 {
			res := agui.Event{
				Type:       agui.EventToolCallResult,
				ToolCallID: string(tu.ToolCallId),
				MessageID:  string(tu.ToolCallId),
				Role:       "tool",
				Content:    toolResultText(tu),
			}
			return []agui.Event{evt, res}
		}
		return []agui.Event{evt}

	case u.Plan != nil:
		// План агента отдаётся снимком состояния целиком: ACP-контракт требует, чтобы
		// клиент заменял план полностью при каждом обновлении.
		return []agui.Event{{
			Type:     agui.EventStateSnapshot,
			Snapshot: map[string]any{"plan": u.Plan.Entries},
		}}

	case u.UsageUpdate != nil:
		uu := u.UsageUpdate
		usage := &agui.Usage{Used: uu.Used, Size: uu.Size}
		if uu.Cost != nil {
			usage.Cost = &agui.Cost{Amount: uu.Cost.Amount, Currency: uu.Cost.Currency}
		}
		// В каноне AG-UI нет типа для расхода контекста — передаём его как CUSTOM.
		return []agui.Event{{Type: agui.EventCustom, Name: agui.CustomUsageName, Value: usage}}

	default:
		// Остальные обновления (режимы, session_info и unstable-варианты) в текущем
		// подмножестве не транслируются.
		return nil
	}
}

// emitUserMessage транслирует реплику пользователя (ACP user_message_chunk) в
// самодостаточную тройку TEXT_MESSAGE_START/CONTENT/END с Role="user". В отличие от
// streamText сообщение приходит целиком и не стримится по чанкам, поэтому собственный
// messageId не смешивается с открытыми потоками ассистента (c.stream не трогаем).
//
// Перед сообщением закрываем открытые потоки ассистента/размышлений: реплика
// пользователя начинает новый turn и не должна перемежаться с незакрытым ответом.
//
// На живой ввод (c.promptActive) трансляцию подавляем: пользовательское сообщение уже
// отрисовано оптимистично на фронте (POST /api/ag-ui/run), и его дубликат был бы лишним.
// user_message_chunk нужен лишь при воспроизведении истории через session/load, когда
// живого Prompt нет. Открытые потоки при этом всё равно закрываем.
func (c *Client) emitUserMessage(id, text string) []agui.Event {
	evts := c.finishStreams()
	if c.promptActive {
		return evts
	}
	return append(evts,
		agui.Event{Type: agui.EventTextMessageStart, MessageID: id, Role: "user"},
		agui.Event{Type: agui.EventTextMessageContent, MessageID: id, Delta: text},
		agui.Event{Type: agui.EventTextMessageEnd, MessageID: id},
	)
}

// availableCommandsEvent транслирует список slash-команд агента (ACP
// available_commands_update) в CUSTOM-событие AG-UI. Из вариативного input агента берём
// только подсказку по свободному вводу (Unstructured.Hint) — единственную форму,
// определённую контрактом.
func availableCommandsEvent(u *acpsdk.SessionAvailableCommandsUpdate) agui.Event {
	cmds := make([]agui.AvailableCommand, 0, len(u.AvailableCommands))
	for _, ac := range u.AvailableCommands {
		cmd := agui.AvailableCommand{Name: ac.Name, Description: ac.Description}
		if ac.Input != nil && ac.Input.Unstructured != nil {
			cmd.Hint = ac.Input.Unstructured.Hint
		}
		cmds = append(cmds, cmd)
	}
	return agui.Event{
		Type:  agui.EventCustom,
		Name:  agui.CustomCommandsName,
		Value: agui.AvailableCommands{Commands: cmds},
	}
}

// streamText продвигает потоковое сообщение ассистента чанком id с текстом delta.
// Закрывает встречный поток размышлений, при смене id закрывает предыдущее сообщение
// (END) и открывает новое (START), затем эмитит CONTENT.
func (c *Client) streamText(id, delta string) []agui.Event {
	var evts []agui.Event
	// Чанк текста закрывает открытый поток размышлений, чтобы сообщения не перемежались.
	evts = append(evts, c.closeReasoning()...)

	if c.stream.textID != id {
		if c.stream.textID != "" {
			evts = append(evts, agui.Event{Type: agui.EventTextMessageEnd, MessageID: c.stream.textID})
		}
		c.stream.textID = id
		evts = append(evts, agui.Event{Type: agui.EventTextMessageStart, MessageID: id, Role: "assistant"})
	}
	evts = append(evts, agui.Event{
		Type:      agui.EventTextMessageContent,
		MessageID: id,
		Delta:     delta,
	})
	return evts
}

// streamReasoning продвигает блок размышлений чанком id с текстом delta. Закрывает
// встречный текстовый поток, при смене id закрывает предыдущий блок (REASONING_*_END)
// и открывает новый (REASONING_START + REASONING_MESSAGE_START), затем эмитит CONTENT.
func (c *Client) streamReasoning(id, delta string) []agui.Event {
	var evts []agui.Event
	// Чанк размышлений закрывает открытый текстовый поток.
	evts = append(evts, c.closeText()...)

	if c.stream.thoughtID != id {
		if c.stream.thoughtID != "" {
			evts = append(evts, c.closeReasoning()...)
		}
		c.stream.thoughtID = id
		evts = append(evts,
			agui.Event{Type: agui.EventReasoningStart, MessageID: id},
			agui.Event{Type: agui.EventReasoningMessageStart, MessageID: id, Role: "reasoning"},
		)
	}
	evts = append(evts, agui.Event{
		Type:      agui.EventReasoningMessageContent,
		MessageID: id,
		Delta:     delta,
	})
	return evts
}

// finishStreams закрывает все открытые потоковые сообщения (текст и размышление).
// Вызывается перед tool call и по завершении turn'а, чтобы клиент перестал показывать
// индикатор стриминга у этих сообщений.
func (c *Client) finishStreams() []agui.Event {
	var evts []agui.Event
	evts = append(evts, c.closeReasoning()...)
	evts = append(evts, c.closeText()...)
	return evts
}

// closeText эмитит TEXT_MESSAGE_END для открытого текстового сообщения и сбрасывает id.
func (c *Client) closeText() []agui.Event {
	if c.stream.textID == "" {
		return nil
	}
	evt := agui.Event{Type: agui.EventTextMessageEnd, MessageID: c.stream.textID}
	c.stream.textID = ""
	return []agui.Event{evt}
}

// closeReasoning закрывает открытый блок размышлений парой REASONING_MESSAGE_END +
// REASONING_END (канон требует закрыть и сообщение, и сам блок) и сбрасывает id.
func (c *Client) closeReasoning() []agui.Event {
	if c.stream.thoughtID == "" {
		return nil
	}
	id := c.stream.thoughtID
	c.stream.thoughtID = ""
	return []agui.Event{
		{Type: agui.EventReasoningMessageEnd, MessageID: id},
		{Type: agui.EventReasoningEnd, MessageID: id},
	}
}

// toolResultText извлекает текстовую полезную нагрузку результата tool call: предпочитает
// сырой вывод инструмента (сериализуя его в JSON), иначе склеивает текст контент-блоков.
// Канон ждёт здесь строку (TOOL_CALL_RESULT.content), а не объект.
func toolResultText(tu *acpsdk.SessionToolCallUpdate) string {
	if tu.RawOutput != nil {
		return rawJSON(tu.RawOutput)
	}
	var out string
	for _, block := range tu.Content {
		if block.Content != nil {
			out += contentText(block.Content.Content)
		}
	}
	if out != "" {
		return out
	}
	return rawJSON(tu.Content)
}

// rawJSON сериализует произвольное значение в компактную JSON-строку. При ошибке
// сериализации возвращает пустую строку: событие всё равно несёт связующий toolCallId,
// а отсутствие аргументов/результата не должно прерывать turn агента.
func rawJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}

// contentText извлекает текст из ACP-блока контента. Нетекстовые блоки (изображения,
// ресурсы) в текстовом потоке не разворачиваются и дают пустую строку.
func contentText(c acpsdk.ContentBlock) string {
	if c.Text != nil {
		return c.Text.Text
	}
	return ""
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
