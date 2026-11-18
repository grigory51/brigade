package agui

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"

	acpsdk "github.com/coder/acp-go-sdk"

	"github.com/grigory51/brigade/backend/internal/acp"
	aguimodel "github.com/grigory51/brigade/backend/internal/agui"
)

// errStub — стабильная ошибка для проверки ветки RUN_ERROR.
var errStub = errors.New("prompt failed")

// Этот же поток проходит через настоящий HttpAgent в agui-wire.test.ts.
func TestRunWireFrames(t *testing.T) {
	data, err := os.ReadFile("testdata/run-events.json")
	if err != nil {
		t.Fatal(err)
	}
	var frames []map[string]any
	if err := json.Unmarshal(data, &frames); err != nil {
		t.Fatal(err)
	}
	for _, want := range frames {
		// Старый журнал содержит delta даже у событий без текстового фрагмента.
		old := make(map[string]any, len(want)+1)
		old["delta"] = ""
		for k, v := range want {
			old[k] = v
		}
		if old["type"] == "TOOL_CALL_START" {
			old["parentMessageId"] = "legacy-anchor"
		}
		if old["type"] == "TOOL_CALL_RESULT" {
			delete(old, "content")
		}
		raw, err := json.Marshal(old)
		if err != nil {
			t.Fatal(err)
		}
		var event aguimodel.Event
		if err := json.Unmarshal(raw, &event); err != nil {
			t.Fatal(err)
		}
		if event.Type == aguimodel.EventMessagesSnapshot && len(want["messages"].([]any)) == 0 {
			event.Messages = acp.MessagesSnapshot(nil)
		}
		if event.Type == aguimodel.EventCustom {
			event = aguimodel.Event{Type: aguimodel.EventPermissionRequest, Permission: &aguimodel.PermissionRequest{
				ID: "p", Title: "Confirm", Options: []aguimodel.PermissionOption{},
			}}
		}
		rec := httptest.NewRecorder()
		rn := &run{w: rec, flusher: rec, threadID: "thread", runID: "run", cancel: func() {}}
		if event.Type == aguimodel.EventRunFinished {
			rn.finish("end_turn", "1.0")
		} else if err := rn.sink(event); err != nil {
			t.Fatal(err)
		}
		body := rec.Body.String()
		rawFrame, ok := strings.CutPrefix(body, "data: ")
		if !ok || !strings.HasSuffix(rawFrame, "\n\n") || !rec.Flushed {
			t.Fatalf("invalid SSE frame: %q", body)
		}
		var got map[string]any
		if err := json.Unmarshal([]byte(rawFrame), &got); err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("frame = %s, want %v (err=%v)", rawFrame, want, err)
		}
	}
}

func TestRunCancelledOutcome(t *testing.T) {
	for _, version := range []string{"", "1.0"} {
		b := &fakeBindable{promptStopReason: "cancelled"}
		body := serveInput(t, b, runAgentInput{
			ProtocolVersion: version,
			Messages:        []inputMessage{{Role: "user", Content: json.RawMessage(`"prompt"`)}},
		})
		if !strings.Contains(body, `"stopReason":"cancelled"`) ||
			strings.Contains(body, `"outcome"`) != (version == "1.0") ||
			(version == "1.0" && !strings.Contains(body, `"type":"cancelled"`)) {
			t.Fatalf("protocol %q: %s", version, body)
		}
	}
}

func TestRunInputContent(t *testing.T) {
	for _, tc := range []struct {
		name, input, want string
		wantErr           bool
	}{
		{"legacy string", `{"messages":[{"role":"user","content":" hello "}]}`, "hello", false},
		{"parts after structured history", `{"messages":[{"role":"user","content":[{"type":"image"}]},{"role":"tool","content":[{"type":"text","text":"result"}]},{"role":"activity","content":{}},{"role":"user","content":[{"type":"text","text":" a "},{"type":"text","text":"b " }]}]}`, "a b", false},
		{"attachment not dropped", `{"messages":[{"role":"user","content":[{"type":"text","text":"see image"},{"type":"image"}]}]}`, "", true},
		{"attachment not replayed", `{"messages":[{"role":"user","content":[{"type":"image"}]}]}`, "", true},
		{"missing text", `{"messages":[{"role":"user","content":[{"type":"text"}]}]}`, "", true},
		{"explicit replay ignores old media", `{"forwardedProps":{"runConfig":{"brigadeReplay":true}},"messages":[{"role":"user","content":[{"type":"image"}]}]}`, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var in runAgentInput
			if err := json.Unmarshal([]byte(tc.input), &in); err != nil {
				t.Fatal(err)
			}
			got, err := in.promptText()
			if got != tc.want || (err != nil) != tc.wantErr {
				t.Fatalf("prompt = %q, err = %v", got, err)
			}
		})
	}
}

func TestReplayDropsLegacyToolAnchor(t *testing.T) {
	w := httptest.NewRecorder()
	rn := &run{w: w, flusher: w, cancel: func() {}}
	if err := rn.send(aguimodel.Event{Type: aguimodel.EventToolCallStart, ToolCallID: "tool", ParentMessageID: "first-text"}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(w.Body.String(), "parentMessageId") {
		t.Fatal("legacy parent anchor changes chronological order")
	}
}

// fakeBindable — тестовая реализация Bindable. Фиксирует переданный Prompt-текст и
// факт вызова FinishStreams; Prompt возвращает заранее заданные stopReason/err.
type fakeBindable struct {
	promptStopReason string
	promptErr        error

	promptCalled bool
	promptText   string
	promptCtxErr error // ctx.Err() на входе Prompt: проверка развязки turn'а от обрыва клиента
	finishCalled bool
	cancelCalled bool
	cancelErr    error
	statuses     []bool
	replayBound  bool
	// calls — порядок вызовов Bind/Prompt/FinishStreams для проверки, что стрим-стейт
	// закрывается ДО привязки к новому sink (см. TestServePromptFinishesStreamsBeforeBind).
	calls []string
	sink  acp.EventSink

	eventsOnBind   []aguimodel.Event
	eventsOnFinish []aguimodel.Event
}

func (b *fakeBindable) Bind(sink acp.EventSink, resolver acp.PermissionResolver) (unbind func()) {
	b.sink = sink
	b.calls = append(b.calls, "Bind")
	b.sendEvents(b.eventsOnBind)
	return func() {}
}

func (b *fakeBindable) BindReplay(sink acp.EventSink, resolver acp.PermissionResolver) (unbind func()) {
	b.sink = sink
	b.replayBound = true
	b.calls = append(b.calls, "BindReplay")
	b.sendEvents(b.eventsOnBind)
	return func() {}
}

func (b *fakeBindable) Prompt(ctx context.Context, text string, onTurnStart func()) (string, error) {
	// Хук вызывается под turn-барьером ДО фактической отправки агенту: воспроизводим это,
	// вызывая onTurnStart до записи "Prompt", чтобы порядок совпадал с боевым (FinishStreams
	// и Bind из хука предшествуют самому Prompt).
	if onTurnStart != nil {
		onTurnStart()
	}
	b.promptCalled = true
	b.promptText = text
	// Фиксируем состояние ctx на входе: боевой serve обязан передать сюда неотменяемый
	// контекст (context.WithoutCancel), чтобы обрыв клиента не сворачивал turn агента.
	b.promptCtxErr = ctx.Err()
	b.calls = append(b.calls, "Prompt")
	return b.promptStopReason, b.promptErr
}

func (b *fakeBindable) Cancel(ctx context.Context) error {
	b.cancelCalled = true
	return b.cancelErr
}

func (b *fakeBindable) FinishStreams() {
	b.finishCalled = true
	b.calls = append(b.calls, "FinishStreams")
	b.sendEvents(b.eventsOnFinish)
}
func (b *fakeBindable) sendEvents(events []aguimodel.Event) {
	for _, event := range events {
		_ = b.sink(event)
	}
}
func (b *fakeBindable) Messages(context.Context) ([]acp.Message, error) { return nil, nil }
func (b *fakeBindable) Commands() []aguimodel.AvailableCommand          { return nil }
func (b *fakeBindable) ConfigOptions() []acpsdk.SessionConfigOption     { return nil }
func (b *fakeBindable) SetConfigOption(context.Context, string, string) ([]acpsdk.SessionConfigOption, error) {
	return nil, nil
}
func (b *fakeBindable) Status() (bool, int) {
	if len(b.statuses) == 0 {
		return false, 0
	}
	status := b.statuses[0]
	b.statuses = b.statuses[1:]
	return status, 0
}

// flushRecorder — httptest.ResponseRecorder не реализует http.Flusher, а run.serve
// требует Flusher. Оборачиваем запись в буфер собственным типом с no-op Flush.
type flushRecorder struct {
	header http.Header
	body   strings.Builder
	status int
}

func newFlushRecorder() *flushRecorder {
	return &flushRecorder{header: make(http.Header)}
}

func (r *flushRecorder) Header() http.Header { return r.header }
func (r *flushRecorder) Write(p []byte) (int, error) {
	return r.body.Write(p)
}
func (r *flushRecorder) WriteHeader(status int) { r.status = status }
func (r *flushRecorder) Flush()                 {}

// serveInput прогоняет run.serve на фейковом Bindable и возвращает тело SSE-потока.
func serveInput(t *testing.T, b Bindable, in runAgentInput) string {
	t.Helper()
	return serveInputCtx(t, context.Background(), b, in)
}

// serveInputCtx — вариант с явным родительским контекстом (эмуляция обрыва клиента:
// отменённый ctx = закрытое HTTP-соединение).
func serveInputCtx(t *testing.T, ctx context.Context, b Bindable, in runAgentInput) string {
	rec := newFlushRecorder()
	perms := NewPermissionStore()
	t.Helper()
	text, err := in.promptText()
	if err != nil {
		t.Fatal(err)
	}
	newRun(ctx, rec, rec, b, perms, "t", "r").serve(text, in.ProtocolVersion)
	return rec.body.String()
}

func TestHeartbeatIsSSEComment(t *testing.T) {
	rec := newFlushRecorder()
	rn := newRun(context.Background(), rec, rec, &fakeBindable{}, NewPermissionStore(), "t", "r")
	defer rn.cancel()
	if err := rn.writeHeartbeat(); err != nil {
		t.Fatal(err)
	}
	if got := rec.body.String(); got != ": keepalive\n\n" {
		t.Fatalf("heartbeat = %q", got)
	}
}

// TestServeReplay проверяет replay-ветку: без пользовательского сообщения агент не
// вызывается (Prompt), поток закрывается FinishStreams + RUN_FINISHED со stopReason
// "replay".
func TestServeReplay(t *testing.T) {
	b := &fakeBindable{}
	body := serveInput(t, b, runAgentInput{ThreadID: "t", RunID: "r"})

	if b.promptCalled {
		t.Error("Prompt вызван в replay-прогоне, want не вызван")
	}
	if !b.replayBound {
		t.Error("replay-прогон не запросил снимок активного turn")
	}
	if !b.finishCalled {
		t.Error("FinishStreams не вызван в replay-прогоне")
	}
	if !strings.Contains(body, `"RUN_STARTED"`) {
		t.Errorf("в потоке нет RUN_STARTED:\n%s", body)
	}
	if !strings.Contains(body, `"RUN_FINISHED"`) {
		t.Errorf("в потоке нет RUN_FINISHED:\n%s", body)
	}
	if !strings.Contains(body, `"stopReason":"replay"`) {
		t.Errorf("в потоке нет stopReason replay:\n%s", body)
	}
}

func TestServeExplicitReplayDoesNotRepeatLastUserMessage(t *testing.T) {
	b := &fakeBindable{}
	in := runAgentInput{
		ThreadID: "t",
		RunID:    "r",
		Messages: []inputMessage{{Role: "user", Content: json.RawMessage(`"не повторять"`)}},
	}
	in.ForwardedProps.RunConfig.BrigadeReplay = true
	serveInput(t, b, in)

	if b.promptCalled {
		t.Fatal("Prompt вызван при явном replay, последнее user-сообщение было бы отправлено повторно")
	}
}

type failedReplay struct{ fakeBindable }

func (b *failedReplay) BindReplayWithFinish(acp.EventSink, acp.PermissionResolver) (func(), func(context.Context) error) {
	return func() {}, func(context.Context) error { return errors.New("snapshot interrupted") }
}

func TestReplayStreamErrorIsNotSuccessfulCompletion(t *testing.T) {
	body := serveInput(t, &failedReplay{}, runAgentInput{})
	if !strings.Contains(body, "RUN_ERROR") || !strings.Contains(body, "snapshot interrupted") || strings.Contains(body, "RUN_FINISHED") {
		t.Fatalf("unexpected replay outcome: %s", body)
	}
}

func TestServeReplayWaitsForActivePrompt(t *testing.T) {
	b := &fakeBindable{statuses: []bool{true, false}}
	body := serveInput(t, b, runAgentInput{ThreadID: "t", RunID: "r"})
	if len(b.statuses) != 0 {
		t.Fatal("replay не дождался завершения Prompt")
	}
	if !strings.Contains(body, `"RUN_FINISHED"`) {
		t.Errorf("в потоке нет RUN_FINISHED:\n%s", body)
	}
}

// TestServePrompt проверяет обычный прогон: пользовательское сообщение передаётся в
// Prompt, а его stopReason уходит в RUN_FINISHED.
func TestServePrompt(t *testing.T) {
	b := &fakeBindable{promptStopReason: "end_turn"}
	body := serveInput(t, b, runAgentInput{
		ThreadID: "t",
		RunID:    "r",
		Messages: []inputMessage{{Role: "user", Content: json.RawMessage(`"привет"`)}},
	})

	if !b.promptCalled {
		t.Fatal("Prompt не вызван при пользовательском сообщении")
	}
	if b.promptText != "привет" {
		t.Errorf("Prompt text = %q, want %q", b.promptText, "привет")
	}
	if !strings.Contains(body, `"stopReason":"end_turn"`) {
		t.Errorf("в потоке нет stopReason end_turn:\n%s", body)
	}
}

func TestServeCompletesResultlessToolCall(t *testing.T) {
	b := &fakeBindable{
		promptStopReason: "end_turn",
		eventsOnBind: []aguimodel.Event{
			{Type: aguimodel.EventToolCallStart, ToolCallID: "silent", ToolCallName: "notify"},
			{Type: aguimodel.EventToolCallEnd, ToolCallID: "silent"},
		},
	}
	body := serveInput(t, b, runAgentInput{
		ProtocolVersion: "1.0",
		Messages:        []inputMessage{{Role: "user", Content: json.RawMessage(`"prompt"`)}},
	})

	frames := sseFrames(t, body)
	end := sseEventIndex(frames, aguimodel.EventToolCallEnd, "silent")
	result := sseEventIndex(frames, aguimodel.EventToolCallResult, "silent")
	finished := sseEventIndex(frames, aguimodel.EventRunFinished, "")
	if end == -1 || result == -1 || finished == -1 ||
		sseEventCount(frames, aguimodel.EventToolCallResult, "silent") != 1 ||
		!(end < result && result < finished) {
		t.Fatalf("resultless tool call was not completed before RUN_FINISHED:\n%s", body)
	}
	if content, ok := frames[result]["content"].(string); !ok || content != "" {
		t.Fatalf("synthetic result content = %#v, want empty string", frames[result]["content"])
	}
}

func TestServeKeepsLateToolResultAndCompletesReplay(t *testing.T) {
	b := &fakeBindable{
		eventsOnBind: []aguimodel.Event{
			{Type: aguimodel.EventToolCallStart, ToolCallID: "diff", ToolCallName: "edit"},
			{Type: aguimodel.EventToolCallEnd, ToolCallID: "diff"},
			{Type: aguimodel.EventToolCallStart, ToolCallID: "silent", ToolCallName: "notify"},
			{Type: aguimodel.EventToolCallEnd, ToolCallID: "silent"},
		},
		eventsOnFinish: []aguimodel.Event{
			{Type: aguimodel.EventToolCallResult, MessageID: "diff", ToolCallID: "diff", Role: "tool", Content: `{"path":"main.go"}`},
		},
	}
	body := serveInput(t, b, runAgentInput{ProtocolVersion: "1.0"})

	frames := sseFrames(t, body)
	diffResult := sseEventIndex(frames, aguimodel.EventToolCallResult, "diff")
	silentResult := sseEventIndex(frames, aguimodel.EventToolCallResult, "silent")
	finished := sseEventIndex(frames, aguimodel.EventRunFinished, "")
	if diffResult == -1 || silentResult == -1 || finished == -1 ||
		sseEventCount(frames, aguimodel.EventToolCallResult, "diff") != 1 ||
		sseEventCount(frames, aguimodel.EventToolCallResult, "silent") != 1 ||
		frames[diffResult]["content"] != `{"path":"main.go"}` ||
		frames[silentResult]["content"] != "" ||
		frames[finished]["result"].(map[string]any)["stopReason"] != "replay" {
		t.Fatalf("replay did not preserve the late result and complete silent tool call:\n%s", body)
	}
}

func sseFrames(t *testing.T, body string) []map[string]any {
	t.Helper()
	var frames []map[string]any
	for _, frame := range strings.Split(body, "\n\n") {
		if frame == "" {
			continue
		}
		raw, ok := strings.CutPrefix(frame, "data: ")
		if !ok {
			t.Fatalf("invalid SSE frame: %q", frame)
		}
		var event map[string]any
		if err := json.Unmarshal([]byte(raw), &event); err != nil {
			t.Fatal(err)
		}
		frames = append(frames, event)
	}
	return frames
}

func sseEventIndex(frames []map[string]any, eventType aguimodel.EventType, toolCallID string) int {
	for index, event := range frames {
		if event["type"] != string(eventType) {
			continue
		}
		if toolCallID == "" || event["toolCallId"] == toolCallID {
			return index
		}
	}
	return -1
}

func sseEventCount(frames []map[string]any, eventType aguimodel.EventType, toolCallID string) int {
	count := 0
	for _, event := range frames {
		if event["type"] == string(eventType) && (toolCallID == "" || event["toolCallId"] == toolCallID) {
			count++
		}
	}
	return count
}

// TestServePromptFinishesStreamsBeforeBind фиксирует порядок для нового промпта:
// FinishStreams вызывается ДО Bind. Так незакрытые потоки прошлых (в т.ч. фоновых)
// turn'ов закрываются в history до привязки к новому sink, и Bind не переоткрывает их
// старый messageId в поток нового run'а (иначе агрегатор клиента ломает порядок
// сообщений — «ответ с середины»). В replay-ветке порядок обратный (см. ниже).
func TestServePromptFinishesStreamsBeforeBind(t *testing.T) {
	b := &fakeBindable{promptStopReason: "end_turn"}
	serveInput(t, b, runAgentInput{
		ThreadID: "t",
		RunID:    "r",
		Messages: []inputMessage{{Role: "user", Content: json.RawMessage(`"привет"`)}},
	})

	// FinishStreams в onTurnStart (до Bind — закрывает хвост прошлого turn'а) И в конце этого
	// turn'а (до unbind/RUN_FINISHED — закрывает его финальный открытый текст, иначе
	// RUN_FINISHED уходит «поверх открытого текста» и клиент @ag-ui его отвергает).
	want := []string{"FinishStreams", "Bind", "Prompt", "FinishStreams"}
	if strings.Join(b.calls, ",") != strings.Join(want, ",") {
		t.Errorf("порядок вызовов = %v, want %v", b.calls, want)
	}
}

// TestServeReplayFinishesStreamsAfterBind фиксирует обратный порядок для replay
// (reconnect без нового сообщения): BindReplay обязан восстановить активный turn целиком,
// и только потом FinishStreams закрывает его перед RUN_FINISHED.
func TestServeReplayFinishesStreamsAfterBind(t *testing.T) {
	b := &fakeBindable{}
	serveInput(t, b, runAgentInput{ThreadID: "t", RunID: "r"})

	want := []string{"BindReplay", "FinishStreams"}
	if strings.Join(b.calls, ",") != strings.Join(want, ",") {
		t.Errorf("порядок вызовов = %v, want %v", b.calls, want)
	}
}

// TestServePromptError проверяет, что ошибка Prompt закрывает поток RUN_ERROR.
func TestServePromptError(t *testing.T) {
	b := &fakeBindable{promptErr: errStub}
	body := serveInput(t, b, runAgentInput{
		ThreadID: "t",
		RunID:    "r",
		Messages: []inputMessage{{Role: "user", Content: json.RawMessage(`"привет"`)}},
	})

	if !strings.Contains(body, `"RUN_ERROR"`) {
		t.Errorf("в потоке нет RUN_ERROR:\n%s", body)
	}
	if !strings.Contains(body, errStub.Error()) {
		t.Errorf("RUN_ERROR не несёт текст ошибки:\n%s", body)
	}
}

// TestServePromptSurvivesClientDisconnect фиксирует развязку контуров: обрыв клиента
// (отменённый родительский ctx = закрытое соединение) НЕ должен отменять turn агента.
// serve обязан передать в Prompt неотменяемый контекст, а session/cancel слаться только
// по явному Stop (Cancel), а не по обрыву — иначе работа терялась бы и пользователю
// приходилось писать retry.
func TestServePromptSurvivesClientDisconnect(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // клиент оборвал соединение: родительский ctx уже отменён

	b := &fakeBindable{promptStopReason: "end_turn"}
	serveInputCtx(t, ctx, b, runAgentInput{
		ThreadID: "t",
		RunID:    "r",
		Messages: []inputMessage{{Role: "user", Content: json.RawMessage(`"привет"`)}},
	})

	if !b.promptCalled {
		t.Fatal("Prompt не вызван при обрыве клиента")
	}
	if b.promptCtxErr != nil {
		t.Errorf("Prompt получил отменённый ctx (%v) — turn убит обрывом клиента; want неотменяемый", b.promptCtxErr)
	}
	if b.cancelCalled {
		t.Error("обрыв клиента дёрнул Cancel — session/cancel шлётся только по явному Stop")
	}
}
