package agui

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
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
	perms    *PermissionStore

	threadID string
	runID    string

	// writeMu сериализует запись в SSE-поток: события доставляются из нескольких горутин
	// (sink ACP-клиента, резолвер permission), а http.ResponseWriter не допускает
	// параллельную запись.
	writeMu sync.Mutex
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
func (rn *run) serve(in runAgentInput) {
	defer rn.cancel()

	h := rn.w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	rn.w.WriteHeader(http.StatusOK)

	// RUN_STARTED обязан быть первым событием потока: клиент @ag-ui/client отклоняет run,
	// если первым приходит что-либо иное. Поэтому открываем поток до Bind, не после.
	rn.send(agui.Event{Type: agui.EventRunStarted, ThreadId: rn.threadID, RunId: rn.runID})

	// Replay-прогон: при открытии треда клиент стартует run без нового пользовательского
	// сообщения (history-адаптер с unstable_resume), чтобы восстановить ленту. Пустой
	// prompt агенту не отправляем — иначе он ответил бы на пустой ввод.
	text := lastUserText(in.Messages)

	if text == "" {
		// Reconnect без нового сообщения: привязываем sink немедленно, чтобы Bind переоткрыл
		// ещё живой поток того же turn'а (иначе последующий CONTENT/END прилетел бы без START
		// и клиент отверг бы событие). Затем закрываем незавершённые потоки: история из
		// session/load может оканчиваться незакрытым сообщением (load не проходит через
		// Prompt), а RUN_FINISHED поверх открытого потока клиент отвергает.
		unbind := rn.bindable.Bind(rn.sink, rn.resolvePermission)
		defer unbind()
		rn.bindable.FinishStreams()
		// Переотправляем висящие запросы разрешения: turn пережил обрыв (Prompt на
		// WithoutCancel) и всё ещё ждёт ответа, но исходный диалог ушёл в оборвавшийся
		// поток. По этим CUSTOM-событиям клиент покажет диалог заново — пользователь
		// ответит, и turn продолжится (см. resolvePermission, PermissionStore.Pending).
		for _, req := range rn.perms.Pending(rn.threadID) {
			req := req
			_ = rn.sink(agui.Event{Type: agui.EventPermissionRequest, Permission: &req})
		}
		rn.send(agui.Event{
			Type:     agui.EventRunFinished,
			ThreadId: rn.threadID,
			RunId:    rn.runID,
			Result:   map[string]any{"stopReason": "replay"},
		})
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
	if unbind != nil {
		unbind()
	}
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

// lastUserText возвращает текст последнего пользовательского сообщения из RunAgentInput —
// это и есть prompt текущего прогона. Пустая строка, если пользовательских сообщений нет.
func lastUserText(messages []inputMessage) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "user" {
			// Обрезаем окаймляющие пробелы и переводы строки: клиенты (в частности
			// мобильный ввод) могут добавлять завершающий \n при отправке. Сообщение
			// из одних пробелов схлопывается в "" и уходит в replay-путь — агенту
			// пустой ввод не отправляется.
			return strings.TrimSpace(messages[i].Content)
		}
	}
	return ""
}
