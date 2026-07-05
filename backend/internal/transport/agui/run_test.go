package agui

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	acpsdk "github.com/coder/acp-go-sdk"

	"github.com/grigory51/brigade/backend/internal/acp"
	aguimodel "github.com/grigory51/brigade/backend/internal/agui"
)

// errStub — стабильная ошибка для проверки ветки RUN_ERROR.
var errStub = errors.New("prompt failed")

// fakeBindable — тестовая реализация Bindable. Фиксирует переданный Prompt-текст и
// факт вызова FinishStreams; Prompt возвращает заранее заданные stopReason/err.
type fakeBindable struct {
	promptStopReason string
	promptErr        error

	promptCalled bool
	promptText   string
	finishCalled bool
	cancelCalled bool
	cancelErr    error
	// calls — порядок вызовов Bind/Prompt/FinishStreams для проверки, что стрим-стейт
	// закрывается ДО привязки к новому sink (см. TestServePromptFinishesStreamsBeforeBind).
	calls []string
}

func (b *fakeBindable) Bind(sink acp.EventSink, resolver acp.PermissionResolver) (unbind func()) {
	b.calls = append(b.calls, "Bind")
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
	b.calls = append(b.calls, "Prompt")
	return b.promptStopReason, b.promptErr
}

func (b *fakeBindable) Cancel(ctx context.Context) error {
	b.cancelCalled = true
	return b.cancelErr
}

func (b *fakeBindable) SetFrontendTools(tools []acp.FrontendTool) {}
func (b *fakeBindable) FinishStreams() {
	b.finishCalled = true
	b.calls = append(b.calls, "FinishStreams")
}
func (b *fakeBindable) Messages() []acp.Message                     { return nil }
func (b *fakeBindable) Commands() []aguimodel.AvailableCommand      { return nil }
func (b *fakeBindable) ConfigOptions() []acpsdk.SessionConfigOption { return nil }
func (b *fakeBindable) SetConfigOption(context.Context, string, string) ([]acpsdk.SessionConfigOption, error) {
	return nil, nil
}
func (b *fakeBindable) Status() (bool, int) { return false, 0 }

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
func serveInput(b *fakeBindable, in runAgentInput) string {
	rec := newFlushRecorder()
	perms := NewPermissionStore()
	newRun(context.Background(), rec, rec, b, perms, "t", "r").serve(in)
	return rec.body.String()
}

// TestServeReplay проверяет replay-ветку: без пользовательского сообщения агент не
// вызывается (Prompt), поток закрывается FinishStreams + RUN_FINISHED со stopReason
// "replay".
func TestServeReplay(t *testing.T) {
	b := &fakeBindable{}
	body := serveInput(b, runAgentInput{ThreadID: "t", RunID: "r"})

	if b.promptCalled {
		t.Error("Prompt вызван в replay-прогоне, want не вызван")
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

// TestServePrompt проверяет обычный прогон: пользовательское сообщение передаётся в
// Prompt, а его stopReason уходит в RUN_FINISHED.
func TestServePrompt(t *testing.T) {
	b := &fakeBindable{promptStopReason: "end_turn"}
	body := serveInput(b, runAgentInput{
		ThreadID: "t",
		RunID:    "r",
		Messages: []inputMessage{{Role: "user", Content: "привет"}},
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

// TestServePromptFinishesStreamsBeforeBind фиксирует порядок для нового промпта:
// FinishStreams вызывается ДО Bind. Так незакрытые потоки прошлых (в т.ч. фоновых)
// turn'ов закрываются в history до привязки к новому sink, и Bind не переоткрывает их
// старый messageId в поток нового run'а (иначе агрегатор клиента ломает порядок
// сообщений — «ответ с середины»). В replay-ветке порядок обратный (см. ниже).
func TestServePromptFinishesStreamsBeforeBind(t *testing.T) {
	b := &fakeBindable{promptStopReason: "end_turn"}
	serveInput(b, runAgentInput{
		ThreadID: "t",
		RunID:    "r",
		Messages: []inputMessage{{Role: "user", Content: "привет"}},
	})

	want := []string{"FinishStreams", "Bind", "Prompt"}
	if strings.Join(b.calls, ",") != strings.Join(want, ",") {
		t.Errorf("порядок вызовов = %v, want %v", b.calls, want)
	}
}

// TestServeReplayFinishesStreamsAfterBind фиксирует обратный порядок для replay
// (reconnect без нового сообщения): Bind обязан переоткрыть ещё живой поток того же
// turn'а, и только потом FinishStreams закрывает его перед RUN_FINISHED.
func TestServeReplayFinishesStreamsAfterBind(t *testing.T) {
	b := &fakeBindable{}
	serveInput(b, runAgentInput{ThreadID: "t", RunID: "r"})

	want := []string{"Bind", "FinishStreams"}
	if strings.Join(b.calls, ",") != strings.Join(want, ",") {
		t.Errorf("порядок вызовов = %v, want %v", b.calls, want)
	}
}

// TestServePromptError проверяет, что ошибка Prompt закрывает поток RUN_ERROR.
func TestServePromptError(t *testing.T) {
	b := &fakeBindable{promptErr: errStub}
	body := serveInput(b, runAgentInput{
		ThreadID: "t",
		RunID:    "r",
		Messages: []inputMessage{{Role: "user", Content: "привет"}},
	})

	if !strings.Contains(body, `"RUN_ERROR"`) {
		t.Errorf("в потоке нет RUN_ERROR:\n%s", body)
	}
	if !strings.Contains(body, errStub.Error()) {
		t.Errorf("RUN_ERROR не несёт текст ошибки:\n%s", body)
	}
}
