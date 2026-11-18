package agui

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/grigory51/brigade/backend/internal/acp"
	"github.com/grigory51/brigade/backend/internal/agui"
)

const sseHeartbeatInterval = 15 * time.Second

// run — состояние одного SSE-прогона: HTTP-поток, привязанный ACP-клиент и контекст
// permission-flow. Жизнь равна одному запросу POST /api/ag-ui/run.
type run struct {
	ctx     context.Context
	cancel  context.CancelFunc
	w       http.ResponseWriter
	flusher http.Flusher

	bindable Bindable
	perms    *PermissionStore

	threadID string
	runID    string

	// writeMu сериализует запись в SSE-поток: события доставляются из нескольких горутин
	// (sink ACP-клиента, резолвер permission), а http.ResponseWriter не допускает
	// параллельную запись.
	writeMu         sync.Mutex
	endedTools      []string
	toolsWithResult map[string]bool
}

func newRun(ctx context.Context, w http.ResponseWriter, flusher http.Flusher, bindable Bindable, perms *PermissionStore, threadID, runID string) *run {
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
func (rn *run) serve(text, protocolVersion string) {
	defer rn.cancel()

	h := rn.w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	rn.w.WriteHeader(http.StatusOK)

	// RUN_STARTED обязан быть первым событием потока: клиент @ag-ui/client отклоняет run,
	// если первым приходит что-либо иное. Поэтому открываем поток до Bind, не после.
	rn.send(agui.Event{Type: agui.EventRunStarted, ThreadId: rn.threadID, RunId: rn.runID})
	go rn.heartbeat()

	// Replay-прогон: при открытии треда клиент стартует run без нового пользовательского
	// сообщения (history-адаптер с unstable_resume), чтобы восстановить ленту. Пустой
	// prompt агенту не отправляем — иначе он ответил бы на пустой ввод.
	if text == "" {
		// Reconnect без нового сообщения: привязываем sink немедленно, чтобы Bind переоткрыл
		// ещё живой поток того же turn'а (иначе последующий CONTENT/END прилетел бы без START
		// и клиент отверг бы событие).
		var unbind func()
		var finishReplay func(context.Context) error
		switch replay := rn.bindable.(type) {
		case interface {
			BindReplayWithFinish(acp.EventSink, acp.PermissionResolver) (func(), func(context.Context) error)
		}:
			unbind, finishReplay = replay.BindReplayWithFinish(rn.sink, rn.resolvePermission)
		case interface {
			BindReplay(acp.EventSink, acp.PermissionResolver) func()
		}:
			unbind = replay.BindReplay(rn.sink, rn.resolvePermission)
		default:
			unbind = rn.bindable.Bind(rn.sink, rn.resolvePermission)
		}
		defer unbind()
		// Переотправляем висящие запросы разрешения: turn пережил обрыв (Prompt на
		// WithoutCancel) и всё ещё ждёт ответа, но исходный диалог ушёл в оборвавшийся
		// поток. По этим CUSTOM-событиям клиент покажет диалог заново — пользователь
		// ответит, и turn продолжится (см. resolvePermission, PermissionStore.Pending).
		for _, req := range rn.perms.Pending(rn.threadID) {
			req := req
			_ = rn.sink(agui.Event{Type: agui.EventPermissionRequest, Permission: &req})
		}
		// Если Prompt пережил перезагрузку страницы, держим replay-SSE открытым до его
		// завершения. Тогда клиент остаётся в обычном isRunning-state со штатной Stop-кнопкой,
		// а не показывает отдельный «фоновый» контрол.
		if generating, _ := rn.bindable.Status(); generating {
			ticker := time.NewTicker(500 * time.Millisecond)
			defer ticker.Stop()
			for generating {
				select {
				case <-rn.ctx.Done():
					return
				case <-ticker.C:
					generating, _ = rn.bindable.Status()
				}
			}
		}
		// История из session/load может оканчиваться незакрытым сообщением. Закрываем его
		// только после живого Prompt, иначе reconnect преждевременно оборвал бы его поток.
		if finishReplay != nil {
			if err := finishReplay(rn.ctx); err != nil {
				if rn.ctx.Err() == nil {
					rn.send(agui.Event{Type: agui.EventRunError, Message: "Не удалось восстановить поток сессии: " + err.Error()})
				}
				return
			}
		} else {
			rn.bindable.FinishStreams()
		}
		if err := rn.completeToolCalls(); err != nil {
			return
		}
		rn.finish("replay", protocolVersion)
		return
	}

	// Новый промпт: привязку sink делаем ВНУТРИ Prompt (хук onTurnStart), а не до него.
	// Хук вызывается под turn-барьером (promptMu) — к этому моменту предыдущий turn
	// полностью завершён и его отложенный cleanup закрыл потоки. Поэтому поток этого
	// прогона привязывается только после того, как хвост предыдущего turn'а физически
	// прекратился, и слипание ответов двух turn'ов невозможно (см. acp.Client.Prompt).
	// FinishStreams в хуке — защитный no-op (предыдущий turn уже закрыл свои потоки).
	// Bind делается на той же горутине, что и serve, поэтому запись unbind без гонки.
	//
	// Контекст turn'а РАЗВЯЗАН от соединения клиента: context.WithoutCancel(rn.ctx).
	// Обрыв клиента (reload, потеря сети, закрытие вкладки) отменяет rn.ctx (он выведен
	// из r.Context() HTTP-запроса); если бы этот ctx уходил в conn.Prompt, ACP-SDK
	// прислал бы агенту session/cancel и turn был бы убит на полуслове — пользователю
	// пришлось бы писать retry. Backend — посредник между контуром AG-UI (клиент) и
	// контуром ACP (агент) и обязан сглаживать обрыв клиента, а не пробрасывать его в
	// агента. Поэтому turn идёт до конца независимо от клиента; события копятся в
	// history (acp.Client.emit доставляет в sink опционально, а в ленту — всегда), и на
	// reconnect лента восстанавливается через AcpService.GetHistory + поллинг статуса.
	// Так уже работает фоновый wakeup-turn (session/registry.go). Явную отмену turn'а
	// (Stop) это не ломает: она идёт отдельным session/cancel через AcpService.Cancel,
	// независимо от ctx (см. acp.Client.Cancel).
	var unbind func()
	stopReason, err := rn.bindable.Prompt(context.WithoutCancel(rn.ctx), text, func() {
		rn.bindable.FinishStreams()
		unbind = rn.bindable.Bind(rn.sink, rn.resolvePermission)
	})
	// Закрываем незавершённые потоки ЭТОГО turn'а (финальный текст остаётся без END — adapter
	// закрывает сообщение лишь на границе turn'а) ДО unbind, пока поток ещё привязан: иначе
	// RUN_FINISHED уйдёт «поверх открытого текста» и клиент @ag-ui его отвергнет
	// (Cannot send 'RUN_FINISHED' while text messages are still active). На daemon-пути
	// FinishStreams дожидается доставки закрытий на SSE (закрытия идут асинхронно через
	// StreamEvents, а RUN_FINISHED — напрямую, и без ожидания обгонял бы их).
	rn.bindable.FinishStreams()
	completionErr := rn.completeToolCalls()
	if unbind != nil {
		unbind()
	}
	if completionErr != nil {
		return
	}
	if err != nil {
		rn.send(agui.Event{Type: agui.EventRunError, Message: err.Error()})
		return
	}
	rn.finish(stopReason, protocolVersion)
}

func (rn *run) finish(stopReason, protocolVersion string) {
	evt := agui.Event{
		Type:     agui.EventRunFinished,
		ThreadId: rn.threadID,
		RunId:    rn.runID,
		Result:   map[string]any{"stopReason": stopReason},
	}
	if protocolVersion == "1.0" {
		// ACP выполняет инструменты сам. Отсутствие результата у завершённого
		// вызова не означает, что клиент должен выполнить этот инструмент.
		evt.Outcome = map[string]any{"type": "success", "pendingToolCallIds": []string{}}
		if stopReason == "cancelled" {
			evt.Outcome = map[string]any{"type": "cancelled"}
		}
	}
	rn.send(evt)
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
	// Якорь из старых журналов привязывает все вызовы к первому тексту turn
	// и нарушает порядок reasoning и карточек при переподключении.
	if evt.Type == agui.EventToolCallStart {
		evt.ParentMessageID = ""
	}
	rn.writeMu.Lock()
	defer rn.writeMu.Unlock()

	switch evt.Type {
	case agui.EventToolCallEnd:
		rn.endedTools = append(rn.endedTools, evt.ToolCallID)
	case agui.EventToolCallResult:
		if rn.toolsWithResult == nil {
			rn.toolsWithResult = make(map[string]bool)
		}
		rn.toolsWithResult[evt.ToolCallID] = true
	}
	return rn.sendLocked(evt)
}

// completeToolCalls закрывает протокольную семантику server-side инструментов. Вызывается
// только после FinishStreams: настоящий поздний результат успевает попасть в поток и
// помечает вызов завершённым, поэтому пустой синтетический результат не дублирует его.
func (rn *run) completeToolCalls() error {
	rn.writeMu.Lock()
	defer rn.writeMu.Unlock()

	for _, id := range rn.endedTools {
		if rn.toolsWithResult[id] {
			continue
		}
		if err := rn.sendLocked(agui.Event{
			Type:       agui.EventToolCallResult,
			MessageID:  id,
			ToolCallID: id,
			Role:       "tool",
		}); err != nil {
			return err
		}
	}
	rn.endedTools = nil
	return nil
}

// sendLocked сериализует событие в SSE-кадр. writeMu должен быть удержан вызывающей
// функцией, чтобы порядок событий и статус tool call оставались едиными.
func (rn *run) sendLocked(evt agui.Event) error {
	// Журнал демона и SSE имеют разные требования к пустым значениям:
	// AG-UI 1.x требует поле даже для пустой дельты или результата, но
	// посторонние поля на других типах событий отправлять нельзя.
	wire := struct {
		agui.Event
		Delta        *string `json:"delta,omitempty"`
		ToolCallName *string `json:"toolCallName,omitempty"`
		Content      *string `json:"content,omitempty"`
		Value        *any    `json:"value,omitempty"`
	}{Event: evt}
	switch evt.Type {
	case agui.EventTextMessageContent, agui.EventReasoningMessageContent, agui.EventToolCallArgs:
		wire.Delta = &evt.Delta
	case agui.EventToolCallStart:
		wire.ToolCallName = &evt.ToolCallName
	case agui.EventToolCallResult:
		wire.Content = &evt.Content
	case agui.EventCustom:
		wire.Value = &evt.Value
	case agui.EventMessagesSnapshot:
		if wire.Messages == nil {
			wire.Messages = []any{}
		}
	}
	data, err := json.Marshal(wire)
	if err != nil {
		return err
	}

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

// heartbeat не даёт reverse proxy закрыть SSE во время долгого молчаливого tool call.
// SSE-комментарий игнорируется клиентом и не вмешивается в порядок AG-UI событий.
func (rn *run) heartbeat() {
	ticker := time.NewTicker(sseHeartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-rn.ctx.Done():
			return
		case <-ticker.C:
			if err := rn.writeHeartbeat(); err != nil {
				rn.cancel()
				return
			}
		}
	}
}

func (rn *run) writeHeartbeat() error {
	rn.writeMu.Lock()
	defer rn.writeMu.Unlock()
	if _, err := rn.w.Write([]byte(": keepalive\n\n")); err != nil {
		return err
	}
	rn.flusher.Flush()
	return nil
}

// resolvePermission реализует acp.PermissionResolver: отдаёт клиенту запрос разрешения
// (через sink → CUSTOM) и блокируется до ответа (AcpService.ResolvePermission с тем же id)
// либо до отмены. Возвращает выбранный OptionID; при отмене — ошибку (исход cancelled).
//
// Обрыв клиента НЕ отменяет ожидание (не селектим на rn.ctx): turn идёт на неотменяемом
// контексте (Prompt через WithoutCancel), поэтому permission переживает дисконнект —
// запрос остаётся в сторе, а на reconnect переотправляется диалог (см. serve replay-ветку).
// Иначе Write и прочие действия, требующие подтверждения, молча срывались бы при обрыве
// «в дороге». Надёжный разрыв висящего ожидания даёт явный Stop: AcpService.Cancel зовёт
// PermissionStore.CancelPending, доставляя пустую строку (не валидный OptionID).
func (rn *run) resolvePermission(ctx context.Context, req agui.PermissionRequest) (string, error) {
	ch, release := rn.perms.Register(rn.threadID, req.ID, req)
	defer release()

	// Доставка диалога best-effort: ошибка записи (клиент отвалился) НЕ отменяет ожидание.
	_ = rn.sink(agui.Event{Type: agui.EventPermissionRequest, Permission: &req})

	select {
	case decision := <-ch:
		// Пустая строка — сигнал отмены (CancelPending на явный Stop); реальный OptionID
		// непустой. Трактуем как cancelled на стороне ACP-вызова.
		if decision == "" {
			return "", context.Canceled
		}
		return decision, nil
	case <-ctx.Done():
		// Страховка: закрытие ACP-сессии / смерть соединения свёртывает ожидание, даже
		// если ни ответа, ни явной отмены не пришло. Обрыв HTTP-клиента сюда НЕ попадает
		// (ctx — контекст ACP-запроса, не клиентского соединения).
		return "", ctx.Err()
	}
}
