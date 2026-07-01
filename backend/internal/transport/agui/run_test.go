package agui

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

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

	promptCalled   bool
	promptText     string
	finishCalled   bool
}

func (b *fakeBindable) Bind(sink acp.EventSink, resolver acp.PermissionResolver) (unbind func()) {
	return func() {}
}

func (b *fakeBindable) Prompt(ctx context.Context, text string) (string, error) {
	b.promptCalled = true
	b.promptText = text
	return b.promptStopReason, b.promptErr
}

func (b *fakeBindable) SetFrontendTools(tools []acp.FrontendTool) {}
func (b *fakeBindable) FinishStreams()                            { b.finishCalled = true }
func (b *fakeBindable) Messages() []acp.Message                   { return nil }
func (b *fakeBindable) Commands() []aguimodel.AvailableCommand    { return nil }

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
	perms := newPermissionStore()
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
