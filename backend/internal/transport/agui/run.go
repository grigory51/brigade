package agui

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"

	"github.com/grigory51/brigade/backend/internal/agui"
)

// run — состояние одного SSE-прогона: HTTP-поток, привязанный ACP-клиент и контекст
// permission-flow. Жизнь равна одному запросу POST /api/ag-ui/run.
type run struct {
	ctx     context.Context
	cancel  context.CancelFunc
	w       http.ResponseWriter
	flusher http.Flusher

	bindable Bindable
	perms    *permissionStore

	threadID string
	runID    string

	// writeMu сериализует запись в SSE-поток: события доставляются из нескольких горутин
	// (sink ACP-клиента, резолвер permission), а http.ResponseWriter не допускает
	// параллельную запись.
	writeMu sync.Mutex
}

func newRun(ctx context.Context, w http.ResponseWriter, flusher http.Flusher, bindable Bindable, perms *permissionStore, threadID, runID string) *run {
	ctx, cancel := context.WithCancel(ctx)
	return &run{
		ctx:      ctx,
		cancel:   cancel,
		w:        w,
		flusher:  flusher,
		bindable: bindable,
		perms:    perms,
		threadID: threadID,
		runID:    runID,
	}
}

// serve проводит весь прогон: устанавливает SSE-заголовки, регистрирует frontend-tools,
// открывает RUN_STARTED, привязывает ACP-клиента к потоку (Bind проигрывает историю) и
// закрывает поток RUN_FINISHED либо RUN_ERROR. Если запрос несёт новое пользовательское
// сообщение — прогоняет turn агента по нему; если нет (replay-прогон при открытии треда) —
// ограничивается воспроизведением истории, не обращаясь к агенту.
func (rn *run) serve(in runAgentInput) {
	defer rn.cancel()

	h := rn.w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	rn.w.WriteHeader(http.StatusOK)

	// Реестр frontend-tools из RunAgentInput.tools[] пробрасывается агенту перед turn'ом:
	// adapter добавляет их в доступные tools, после чего tool_use по ним приходит обратно.
	rn.bindable.SetFrontendTools(toFrontendTools(in.Tools))

	// RUN_STARTED обязан быть первым событием потока: клиент @ag-ui/client отклоняет run,
	// если первым приходит что-либо иное. Поэтому открываем поток до Bind, не после.
	rn.send(agui.Event{Type: agui.EventRunStarted, ThreadId: rn.threadID, RunId: rn.runID})

	// Привязываем ACP-клиента к потоку: накопленная история (в т.ч. восстановленная через
	// session/load после рестарта) синхронно проигрывается в sink сразу за RUN_STARTED,
	// затем идут живые события текущего turn'а.
	unbind := rn.bindable.Bind(rn.sink, rn.resolvePermission)
	defer unbind()

	// Replay-прогон: при открытии треда клиент стартует run без нового пользовательского
	// сообщения (history-адаптер с unstable_resume), чтобы восстановить ленту. Пустой
	// prompt агенту не отправляем — иначе он ответил бы на пустой ввод; историю уже
	// проиграл Bind выше, остаётся лишь закрыть поток.
	text := lastUserText(in.Messages)
	if text == "" {
		// История из session/load может оканчиваться незакрытым потоковым сообщением
		// (load не проходит через Prompt и не закрывает потоки) — закрываем их, чтобы
		// END ушёл в SSE до RUN_FINISHED, иначе клиент отвергнет RUN_FINISHED.
		rn.bindable.FinishStreams()
		rn.send(agui.Event{
			Type:     agui.EventRunFinished,
			ThreadId: rn.threadID,
			RunId:    rn.runID,
			Result:   map[string]any{"stopReason": "replay"},
		})
		return
	}

	stopReason, err := rn.bindable.Prompt(rn.ctx, text)
	if err != nil {
		rn.send(agui.Event{Type: agui.EventRunError, Message: err.Error()})
		return
	}
	rn.send(agui.Event{
		Type:     agui.EventRunFinished,
		ThreadId: rn.threadID,
		RunId:    rn.runID,
		Result:   map[string]any{"stopReason": stopReason},
	})
}

// sink реализует acp.EventSink: доставляет AG-UI-событие клиенту по SSE. Permission-
// запросы перехватываются и отдаются как CUSTOM (см. пакетный комментарий), остальные
// события сериализуются как есть. Ошибка записи отменяет ctx прогона.
func (rn *run) sink(evt agui.Event) error {
	if evt.Type == agui.EventPermissionRequest {
		// Внутренний permission в канон не входит — заворачиваем в CUSTOM, чтобы клиент
		// получил его в общем потоке и ответил отдельным POST /api/ag-ui/permission.
		return rn.send(agui.Event{Type: agui.EventCustom, Name: CustomPermissionName, Value: evt.Permission})
	}
	return rn.send(evt)
}

// send сериализует событие в SSE-кадр `data: {json}\n\n` и сбрасывает буфер. Запись
// сериализована writeMu; ошибка отменяет ctx прогона (дальнейшая доставка бессмысленна).
func (rn *run) send(evt agui.Event) error {
	data, err := json.Marshal(evt)
	if err != nil {
		return err
	}

	rn.writeMu.Lock()
	defer rn.writeMu.Unlock()

	if _, err := rn.w.Write([]byte("data: ")); err != nil {
		rn.cancel()
		return err
	}
	if _, err := rn.w.Write(data); err != nil {
		rn.cancel()
		return err
	}
	if _, err := rn.w.Write([]byte("\n\n")); err != nil {
		rn.cancel()
		return err
	}
	rn.flusher.Flush()
	return nil
}

// resolvePermission реализует acp.PermissionResolver: отдаёт клиенту запрос разрешения
// (через sink → CUSTOM) и блокируется до ответа POST /api/ag-ui/permission с тем же id
// либо до отмены ctx (клиент закрыл поток / turn свёрнут). Возвращает выбранный OptionID;
// при отмене — ошибку (исход cancelled на стороне ACP-вызова).
func (rn *run) resolvePermission(ctx context.Context, req agui.PermissionRequest) (string, error) {
	key := permissionKey(rn.threadID, req.ID)
	ch, release := rn.perms.register(key)
	defer release()

	if err := rn.sink(agui.Event{Type: agui.EventPermissionRequest, Permission: &req}); err != nil {
		return "", err
	}

	select {
	case decision := <-ch:
		return decision, nil
	case <-ctx.Done():
		return "", ctx.Err()
	case <-rn.ctx.Done():
		return "", rn.ctx.Err()
	}
}

// lastUserText возвращает текст последнего пользовательского сообщения из RunAgentInput —
// это и есть prompt текущего прогона. Пустая строка, если пользовательских сообщений нет.
func lastUserText(messages []inputMessage) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "user" {
			return messages[i].Content
		}
	}
	return ""
}
