// Package agui описывает события канонического AG-UI protocol как Go-структуры для
// сериализации в JSON.
//
// AG-UI (Agent-User Interaction protocol) — стандартный формат, который понимают
// клиенты @ag-ui/client (web, assistant-ui) и AG-UI Kotlin SDK (mobile). Бэкенд
// транслирует поток ACP-событий агента в канонический поток AG-UI и отдаёт его по SSE
// (см. internal/transport/agui), чтобы не дублировать логику рендера на каждом клиенте.
//
// Состав событий и точные имена полей соответствуют @ag-ui/core (EventType и
// *EventSchema). Реализовано подмножество, достаточное для режима ACP brigade:
// lifecycle прогона (RUN_*), потоковый текст ассистента (TEXT_MESSAGE_*), размышления
// (REASONING_*), tool calls (TOOL_CALL_*), снимок состояния/плана (STATE_SNAPSHOT) и
// расход контекста (CUSTOM name="usage"). Поле Type сериализуется строкой-дискриминатором.
//
// PermissionRequest/PermissionOption и EventPermissionRequest — внутренний механизм
// human-in-the-loop brigade (ACP RequestPermission), не часть канонического словаря
// AG-UI. SSE однонаправлен, поэтому транспорт agui не сериализует событие как есть, а
// переупаковывает запрос в CUSTOM {name:"permission_request"} клиенту и ждёт ответ
// отдельным POST (см. internal/transport/agui).
package agui

// EventType — тип события AG-UI (дискриминатор в JSON). Значения соответствуют
// @ag-ui/core EventType.
type EventType string

const (
	// EventRunStarted открывает прогон (turn): отправляется до первого события агента,
	// несёт ThreadId/RunId прогона. EventRunFinished закрывает прогон, EventRunError
	// сигнализирует об ошибке его обработки.
	EventRunStarted  EventType = "RUN_STARTED"
	EventRunFinished EventType = "RUN_FINISHED"
	EventRunError    EventType = "RUN_ERROR"

	// EventTextMessageStart открывает потоковое сообщение ассистента: за ним следуют
	// фрагменты EventTextMessageContent (Delta) и закрывающий EventTextMessageEnd с тем
	// же MessageID.
	EventTextMessageStart   EventType = "TEXT_MESSAGE_START"
	EventTextMessageContent EventType = "TEXT_MESSAGE_CONTENT"
	EventTextMessageEnd     EventType = "TEXT_MESSAGE_END"

	// EventReasoningStart открывает блок размышлений агента (ACP agent_thought_chunk):
	// REASONING_START → REASONING_MESSAGE_START → REASONING_MESSAGE_CONTENT (Delta) →
	// REASONING_MESSAGE_END → REASONING_END. Все события блока связаны MessageID.
	EventReasoningStart          EventType = "REASONING_START"
	EventReasoningMessageStart   EventType = "REASONING_MESSAGE_START"
	EventReasoningMessageContent EventType = "REASONING_MESSAGE_CONTENT"
	EventReasoningMessageEnd     EventType = "REASONING_MESSAGE_END"
	EventReasoningEnd            EventType = "REASONING_END"

	// EventToolCallStart открывает tool call (ToolCallID, ToolCallName); EventToolCallArgs
	// передаёт его аргументы строкой-фрагментом JSON (Delta); EventToolCallEnd закрывает;
	// EventToolCallResult несёт результат (MessageID, Content, ToolCallID).
	EventToolCallStart  EventType = "TOOL_CALL_START"
	EventToolCallArgs   EventType = "TOOL_CALL_ARGS"
	EventToolCallEnd    EventType = "TOOL_CALL_END"
	EventToolCallResult EventType = "TOOL_CALL_RESULT"

	// EventStateSnapshot несёт снимок состояния сессии (Snapshot); здесь используется
	// для проброса плана агента (ACP plan) целиком.
	EventStateSnapshot EventType = "STATE_SNAPSHOT"

	// EventCustom — расширяемое событие {name, value}. В каноне AG-UI нет отдельного
	// типа для расхода контекста, поэтому usage_update передаётся как CUSTOM с
	// Name="usage" и Value=Usage (см. CustomUsageName).
	EventCustom EventType = "CUSTOM"

	// EventPermissionRequest — внутренний механизм human-in-the-loop (ACP
	// RequestPermission). Не часть канонического AG-UI: транспорт обрабатывает его
	// отдельно и SSE-клиенту не отправляет (см. пакетный комментарий).
	EventPermissionRequest EventType = "PERMISSION_REQUEST"
)

// CustomUsageName — значение поля Name события EventCustom, несущего расход контекста.
const CustomUsageName = "usage"

// CustomCommandsName — значение поля Name события EventCustom, несущего список
// slash-команд агента (ACP available_commands_update). В каноне AG-UI типа для набора
// доступных команд нет, поэтому передаём его как CUSTOM с Value=AvailableCommands.
const CustomCommandsName = "available_commands"

// CustomA2UIName — значение поля Name события EventCustom, несущего поставку A2UI-
// сообщений (generative UI поверх AG-UI-транспорта). Value — {"messages": [...]},
// где элементы — server→client сообщения A2UI v0.9 (см. internal/a2ui).
const CustomA2UIName = "a2ui"

// CustomConfigOptionsName — значение поля Name события EventCustom, несущего
// актуальный набор конфигурационных опций сессии (модель, режим прав, усилие).
// Value — массив ACP SessionConfigOption (wire-формат ACP).
const CustomConfigOptionsName = "config_options"

// Event — обобщённое событие AG-UI. Незаполненные поля опускаются при сериализации,
// поэтому одна структура покрывает все варианты без отдельных типов на каждый. Имена
// полей в JSON соответствуют каноническим *EventSchema из @ag-ui/core.
type Event struct {
	Type EventType `json:"type"`

	// ThreadId/RunId идентифицируют прогон; заполняются для RUN_STARTED/RUN_FINISHED.
	ThreadId string `json:"threadId,omitempty"`
	RunId    string `json:"runId,omitempty"`

	// MessageID связывает START/CONTENT/END одного сообщения ассистента или блока
	// размышлений; также адресует результат tool call (TOOL_CALL_RESULT.messageId).
	MessageID string `json:"messageId,omitempty"`
	// Delta — очередной фрагмент текста (TEXT_MESSAGE_CONTENT, REASONING_MESSAGE_CONTENT)
	// либо фрагмент JSON-аргументов (TOOL_CALL_ARGS). Без omitempty: канонические Zod-схемы
	// @ag-ui/client объявляют delta обязательным (string), а пустой фрагмент ("") —
	// легитимный контент; omitempty опустил бы поле и клиент отверг бы событие (delta
	// Required). Для событий без delta лишнее поле "" клиентом игнорируется.
	Delta string `json:"delta"`
	// Role — роль автора сообщения ("assistant" для текста, "reasoning" для размышлений,
	// "tool" для результата tool call).
	Role string `json:"role,omitempty"`

	// ToolCallID связывает события одного tool call.
	ToolCallID string `json:"toolCallId,omitempty"`
	// ToolCallName — имя вызываемого инструмента (TOOL_CALL_START).
	ToolCallName string `json:"toolCallName,omitempty"`
	// Content — результат tool call строкой (TOOL_CALL_RESULT.content).
	Content string `json:"content,omitempty"`

	// Snapshot — произвольный снимок состояния для STATE_SNAPSHOT (например, план).
	Snapshot any `json:"snapshot,omitempty"`

	// Result — произвольный итог прогона для RUN_FINISHED (например, stopReason).
	Result any `json:"result,omitempty"`

	// Name/Value — полезная нагрузка EventCustom (например, Name="usage", Value=Usage).
	Name  string `json:"name,omitempty"`
	Value any    `json:"value,omitempty"`

	// Message/Code — текст и код ошибки для RUN_ERROR.
	Message string `json:"message,omitempty"`
	Code    string `json:"code,omitempty"`

	// Permission заполнено только для EventPermissionRequest (внутренний механизм,
	// SSE-клиенту не сериализуется — см. пакетный комментарий).
	Permission *PermissionRequest `json:"-"`
}

// Usage описывает расход контекста и стоимость сессии (ACP usage_update). Передаётся
// как Value события EventCustom с Name="usage".
type Usage struct {
	// Used — токены, занятые в контексте; Size — полный размер окна контекста.
	Used int `json:"used"`
	Size int `json:"size"`
	// Cost — кумулятивная стоимость сессии; nil, если агент её не сообщил.
	Cost *Cost `json:"cost,omitempty"`
}

// Cost — кумулятивная стоимость сессии в указанной валюте.
type Cost struct {
	Amount   float64 `json:"amount"`
	Currency string  `json:"currency"`
}

// AvailableCommands — список slash-команд агента (ACP available_commands_update).
// Передаётся как Value события EventCustom с Name="available_commands".
type AvailableCommands struct {
	Commands []AvailableCommand `json:"commands"`
}

// AvailableCommand — одна slash-команда агента.
type AvailableCommand struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	// Hint — подсказка по свободному вводу после имени команды (ACP unstructured input);
	// пустая, если команда не принимает дополнительный ввод.
	Hint string `json:"hint,omitempty"`
}

// PermissionRequest описывает запрос разрешения, показываемый пользователю.
// Клиент возвращает выбранный OptionID. Это внутренний механизм brigade, не канон AG-UI.
type PermissionRequest struct {
	// ID — идентификатор запроса; клиент возвращает его вместе с решением, чтобы
	// разблокировать соответствующий заблокированный ACP-вызов.
	ID string `json:"id"`
	// Title — человекочитаемое описание действия (заголовок tool call).
	Title string `json:"title"`
	// ToolCallID — связанный tool call, если он есть.
	ToolCallID string `json:"toolCallId,omitempty"`
	// Options — варианты ответа (allow_once/reject_always/...).
	Options []PermissionOption `json:"options"`
}

// PermissionOption — один вариант ответа на запрос разрешения.
type PermissionOption struct {
	// OptionID — идентификатор варианта; именно его клиент присылает как решение.
	OptionID string `json:"optionId"`
	// Name — подпись варианта для пользователя.
	Name string `json:"name"`
	// Kind — характер варианта (allow_once/allow_always/reject_once/reject_always).
	Kind string `json:"kind"`
}
