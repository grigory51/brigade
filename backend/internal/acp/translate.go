package acp

import (
	"encoding/json"
	"regexp"
	"strings"

	acpsdk "github.com/coder/acp-go-sdk"

	"github.com/grigory51/brigade/backend/internal/a2ui"
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

// toolCallState — накопленное состояние одного tool call'а (см. Client.toolCalls).
type toolCallState struct {
	// open — TOOL_CALL_START отправлен, TOOL_CALL_END ещё нет.
	open bool
	// name — заголовок вызова; нужен для переоткрытия START при reconnect (Bind).
	name string
	// result — последний содержательный результат вызова.
	result string
	// isDiff — result несёт структурный diff: он важнее статусных строк и не
	// затирается ими («липкий diff»).
	isDiff bool
	// diffs — структурные diff-блоки вызова для генерации A2UI-поверхности при
	// закрытии (см. closeToolCallEvents).
	diffs []a2ui.DiffData
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

	case u.ConfigOptionUpdate != nil:
		// Снимок опций сессии (модель, режим прав, усилие) изменился на стороне
		// агента. Сохраняем и уведомляем фронт CUSTOM-событием, чтобы селекторы в
		// composer не отставали от фактического состояния.
		c.configOptions = u.ConfigOptionUpdate.ConfigOptions
		return []agui.Event{{
			Type:  agui.EventCustom,
			Name:  agui.CustomConfigOptionsName,
			Value: u.ConfigOptionUpdate.ConfigOptions,
		}}

	case u.AgentMessageChunk != nil:
		id := deref(u.AgentMessageChunk.MessageId)
		return c.streamText(id, contentText(u.AgentMessageChunk.Content))

	case u.AgentThoughtChunk != nil:
		id := deref(u.AgentThoughtChunk.MessageId)
		return c.streamReasoning(id, contentText(u.AgentThoughtChunk.Content))

	case u.ToolCall != nil:
		tc := u.ToolCall
		id := string(tc.ToolCallId)
		if c.toolCalls == nil {
			c.toolCalls = make(map[string]*toolCallState)
		}
		c.toolCalls[id] = &toolCallState{open: true, name: tc.Title}

		evts := c.finishStreams()
		evts = append(evts, agui.Event{
			Type:         agui.EventToolCallStart,
			ToolCallID:   id,
			ToolCallName: tc.Title,
		})
		// Аргументы tool call отдаются строкой-фрагментом JSON (TOOL_CALL_ARGS.delta):
		// канон ждёт здесь сериализованный ввод, а не объект. RawInput — сырой JSON
		// инструмента; передаём его одним фрагментом.
		if tc.RawInput != nil {
			evts = append(evts, agui.Event{
				Type:       agui.EventToolCallArgs,
				ToolCallID: id,
				Delta:      rawJSON(tc.RawInput),
			})
		}
		return evts

	case u.ToolCallUpdate != nil:
		// Агент шлёт несколько tool_call_update на один вызов (промежуточный контент,
		// смена статуса, финальный вывод), а клиент требует РОВНО ОДИН TOOL_CALL_END —
		// повторный END роняет прогон на стороне @ag-ui/client. Поэтому промежуточные
		// обновления только копят результат в toolCalls, а END+RESULT эмитятся один раз —
		// на терминальном статусе (completed/failed). Вызовы, не получившие терминального
		// статуса к концу turn'а, закрывает closeOpenToolCalls.
		tu := u.ToolCallUpdate
		id := string(tu.ToolCallId)
		st := c.toolCalls[id]
		if st == nil {
			// Update для неизвестного вызова (например, START был до рестарта) —
			// заводим состояние, но START не эмитим: клиент отверг бы END без START.
			st = &toolCallState{}
			if c.toolCalls == nil {
				c.toolCalls = make(map[string]*toolCallState)
			}
			c.toolCalls[id] = st
		}

		// Копим содержательный результат. Diff «липнет»: статусная строка
		// («file updated successfully») его не затирает — diff нужен карточке рендера.
		if tu.RawOutput != nil || len(tu.Content) > 0 {
			content := toolResultText(tu)
			if hasDiffContent(tu) {
				st.result, st.isDiff = content, true
				st.diffs = diffData(tu)
			} else if !st.isDiff {
				st.result = content
			}
		}

		terminal := tu.Status != nil &&
			(*tu.Status == acpsdk.ToolCallStatusCompleted || *tu.Status == acpsdk.ToolCallStatusFailed)
		if !terminal || !st.open {
			return nil
		}
		st.open = false
		return closeToolCallEvents(id, st)

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
//
// Синтетические инжекции харнесса — wake-up уведомления о фоновых задачах — приходят
// в контекст агента user-сообщениями (сырой <task-notification>-XML) и неотличимы от
// реплик пользователя по каналу доставки: и живьём, и в реплее session/load (транскрипт
// агента хранит их обычными user-записями, адаптер их не фильтрует). Показывать их от
// имени пользователя нельзя — распознаём по точным маркерам содержимого
// (looksSyntheticNotification) и транслируем системной карточкой: Role="system" с
// человекочитаемой выжимкой. Проверка безусловна (не зависит от фазы реплея/гонок
// поздних нотификаций): точность маркеров исключает ложное срабатывание на реальных
// репликах, а реплей после рестарта рисует те же карточки, что и живой показ.
//
// Замечание: в живой SSE-прогон системная тройка фактически не попадает (фоновый turn
// идёт с sink=nil, живой ввод подавлен promptActive); если путь когда-то откроется,
// агрегатор @ag-ui на клиенте роняет role у TEXT_MESSAGE_START — выжимка отрисуется
// текстом ассистента (косметика, не порча данных).
func (c *Client) emitUserMessage(id, text string) []agui.Event {
	evts := c.finishStreams()
	if c.promptActive {
		return evts
	}
	role := "user"
	if looksSyntheticNotification(text) {
		role = "system"
		text = systemNotificationSummary(text)
	}
	return append(evts,
		agui.Event{Type: agui.EventTextMessageStart, MessageID: id, Role: role},
		agui.Event{Type: agui.EventTextMessageContent, MessageID: id, Delta: text},
		agui.Event{Type: agui.EventTextMessageEnd, MessageID: id},
	)
}

// looksSyntheticNotification распознаёт инжекции харнесса по точным маркерам начала
// текста: <task-notification> (фоновые задачи), <system-reminder> (служебные вставки),
// [SYSTEM NOTIFICATION (префикс-предупреждение перед уведомлением). Намеренно НЕ
// матчим произвольный "<": реальная реплика пользователя может начинаться с XML/HTML
// (вставленный код), и она обязана остаться role=user. Новые форматы инжекций
// добавлять сюда же.
func looksSyntheticNotification(text string) bool {
	t := strings.TrimSpace(text)
	return strings.HasPrefix(t, "<task-notification") ||
		strings.HasPrefix(t, "<system-reminder") ||
		strings.HasPrefix(t, "[SYSTEM NOTIFICATION")
}

// summaryTagRe выделяет содержимое <summary> из wake-up уведомления харнесса
// (<task-notification> несёт краткое описание исхода задачи именно там).
var summaryTagRe = regexp.MustCompile(`(?s)<summary>(.*?)</summary>`)

// xmlTagRe вычищает XML-подобные теги из уведомления, если <summary> не нашёлся.
var xmlTagRe = regexp.MustCompile(`<[^>]*>`)

// systemNotificationSummary приводит синтетическое сообщение харнесса к короткому
// человекочитаемому виду для системной карточки в ленте: берёт содержимое <summary>,
// иначе весь текст; в обоих случаях вычищает остаточные теги (вложенная разметка внутри
// summary — тоже) и усечает до разумной длины.
func systemNotificationSummary(text string) string {
	src := text
	if m := summaryTagRe.FindStringSubmatch(text); m != nil && strings.TrimSpace(m[1]) != "" {
		src = m[1]
	}
	s := strings.TrimSpace(strings.Join(strings.Fields(xmlTagRe.ReplaceAllString(src, " ")), " "))
	if s == "" {
		return "Системное уведомление"
	}
	const maxRunes = 300
	if r := []rune(s); len(r) > maxRunes {
		return string(r[:maxRunes]) + "…"
	}
	return s
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

// closeToolCallEvents формирует закрытие tool call'а: TOOL_CALL_END, при непустом
// результате — TOOL_CALL_RESULT (messageId результата равен toolCallId — ответ
// инструмента самостоятельного messageId не имеет), а при накопленных diff-блоках —
// поставку A2UI-поверхности карточки (CUSTOM a2ui с surfaceId=toolCallId). Клиент с
// A2UI-каталогом рендерит поверхность; клиент без него игнорирует CUSTOM и падает
// обратно на RESULT с diff-JSON.
func closeToolCallEvents(id string, st *toolCallState) []agui.Event {
	evts := []agui.Event{{Type: agui.EventToolCallEnd, ToolCallID: id}}
	if st.result != "" {
		evts = append(evts, agui.Event{
			Type:       agui.EventToolCallResult,
			ToolCallID: id,
			MessageID:  id,
			Role:       "tool",
			Content:    st.result,
		})
	}
	if len(st.diffs) > 0 {
		evts = append(evts, agui.Event{
			Type:  agui.EventCustom,
			Name:  agui.CustomA2UIName,
			Value: map[string]any{"messages": a2ui.DiffSurface(id, st.diffs)},
		})
	}
	return evts
}

// closeOpenToolCalls закрывает tool call'ы, не получившие терминального статуса
// (агент/адаптер не всегда его шлёт): без закрытия клиент навсегда показывал бы
// «крутилку», а RUN_FINISHED поверх открытого вызова был бы отвергнут. Вызывается под
// c.mu в конце turn'а и перед завершением replay-прогона.
func (c *Client) closeOpenToolCalls() []agui.Event {
	var evts []agui.Event
	for id, st := range c.toolCalls {
		if !st.open {
			continue
		}
		st.open = false
		evts = append(evts, closeToolCallEvents(id, st)...)
	}
	return evts
}

// diffData извлекает структурные diff-блоки обновления tool call'а в модель данных
// A2UI-карточки.
func diffData(tu *acpsdk.SessionToolCallUpdate) []a2ui.DiffData {
	var out []a2ui.DiffData
	for _, block := range tu.Content {
		if block.Diff == nil {
			continue
		}
		d := a2ui.DiffData{Path: block.Diff.Path, NewText: block.Diff.NewText}
		if block.Diff.OldText != nil {
			d.OldText = *block.Diff.OldText
		}
		out = append(out, d)
	}
	return out
}

// hasDiffContent сообщает, несёт ли обновление tool call структурный diff-контент
// (ACP ToolCallContent с полем Diff). Такой результат содержательнее статусных строк —
// см. «липкий diff» в translateUpdate.
func hasDiffContent(tu *acpsdk.SessionToolCallUpdate) bool {
	for _, block := range tu.Content {
		if block.Diff != nil {
			return true
		}
	}
	return false
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
