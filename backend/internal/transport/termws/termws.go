// Package termws обслуживает WebSocket-терминалы.
//
// Эндпоинты апгрейдят HTTP-соединение до WebSocket, аутентифицируют клиента по
// одноразовому тикету (?ticket=) и связывают поток псевдотерминала с WebSocket:
//
//   - сервер→клиент: raw-байты pty бинарными фреймами (io.Copy терминал→WS);
//   - клиент→сервер: JSON-сообщения {type:"input",data} | {type:"resize",cols,rows}.
//
// Два эндпоинта:
//
//   - Handler — терминал агента CLI-сессии: живой Handle отдаёт реестр сессий
//     (HandleProvider), жизненный цикл агента этим эндпоинтом не управляется;
//   - ShellHandler — вспомогательный шелл рядом с любой сессией: спавнится на
//     каждое WS-подключение (ShellProvider) и завершается при его разрыве.
package termws

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/coder/websocket"

	"github.com/grigory51/brigade/backend/internal/spawn"
)

// Terminal — минимальный контракт псевдотерминала, который умеет обслуживать serve:
// поток ввода-вывода, изменение размера и хвост вывода для восстановления экрана.
// spawn.Handle удовлетворяет ему структурно.
type Terminal interface {
	io.ReadWriter

	// Resize меняет размер псевдотерминала.
	Resize(cols, rows uint16) error

	// History возвращает копию накопленного хвоста вывода (может быть пустой).
	History() []byte
}

// Shell — терминал вспомогательного шелла вместе с завершением его процесса.
// Terminate вызывается транспортом при разрыве WS-подключения.
type Shell interface {
	Terminal

	// Terminate завершает процесс шелла и освобождает его ресурсы. Идемпотентна.
	Terminate(ctx context.Context) error
}

// ShellProvider спавнит вспомогательный шелл рядом с сессией. Реализуется реестром
// сессий: local-сессия — шелл-процесс хоста в pty (cwd сессии), docker-сессия —
// exec в контейнер сессии. Ошибка означает, что шелл недоступен (сессия не найдена,
// принадлежит другому пользователю или её контейнер не работает).
type ShellProvider interface {
	Shell(ctx context.Context, sessionID, userID string) (Shell, error)
}

// TicketRedeemer проверяет одноразовый WS-тикет, привязанный к session_id, и
// возвращает id пользователя. Удовлетворяется auth.TicketStore.
type TicketRedeemer interface {
	Redeem(token, sessionID string) (userID string, ok bool)
}

// HandleProvider отдаёт активный Handle псевдотерминала по идентификатору сессии.
// Удовлетворяется реестром живых сессий (internal/session, шаг 3). До его готовности
// транспорт компилируется и тестируется против любой реализации этого интерфейса.
type HandleProvider interface {
	// Handle возвращает Handle сессии и принадлежащего ей пользователя, при необходимости
	// пере-подняв мёртвую среду агента (docker-демон убит вне рестарта brigade). ok=false,
	// если сессия неизвестна, принадлежит другому пользователю либо среду поднять не удалось.
	Handle(ctx context.Context, sessionID, userID string) (h spawn.Handle, ok bool)
}

// clientMessage — входящее сообщение от клиента (клиент→сервер).
type clientMessage struct {
	// Type — дискриминатор: "input" (ввод в pty) или "resize" (изменение размера).
	Type string `json:"type"`
	// Data — полезная нагрузка для type=input (текст ввода терминала).
	Data string `json:"data"`
	// Cols, Rows — новый размер терминала для type=resize.
	Cols uint16 `json:"cols"`
	Rows uint16 `json:"rows"`
}

// Handler возвращает http.Handler WS-эндпоинта терминала. Путь регистрируется как
// "GET /ws/terminal/{sessionId}"; sessionId извлекается из PathValue.
func Handler(tickets TicketRedeemer, provider HandleProvider) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sessionID := r.PathValue("sessionId")
		if sessionID == "" {
			http.Error(w, "sessionId не задан", http.StatusBadRequest)
			return
		}

		// Аутентификация до апгрейда: невалидный тикет — отказ обычным HTTP-кодом.
		userID, ok := tickets.Redeem(r.URL.Query().Get("ticket"), sessionID)
		if !ok {
			http.Error(w, "невалидный тикет", http.StatusUnauthorized)
			return
		}

		handle, ok := provider.Handle(r.Context(), sessionID, userID)
		if !ok {
			http.Error(w, "сессия не найдена", http.StatusNotFound)
			return
		}

		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			// Accept уже записал ответ; логирование — на вызывающей стороне сервера.
			return
		}
		// Закрытие по нормальному завершению; CloseNow на случай уже разорванного канала.
		defer conn.CloseNow()

		serve(r.Context(), conn, handle)
	})
}

// ShellHandler возвращает http.Handler WS-эндпоинта вспомогательного шелла. Путь
// регистрируется как "GET /ws/shell/{sessionId}". Шелл живёт ровно столько, сколько
// WS-подключение: спавнится после успешной аутентификации и завершается при разрыве.
func ShellHandler(tickets TicketRedeemer, shells ShellProvider) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sessionID := r.PathValue("sessionId")
		if sessionID == "" {
			http.Error(w, "sessionId не задан", http.StatusBadRequest)
			return
		}

		userID, ok := tickets.Redeem(r.URL.Query().Get("ticket"), sessionID)
		if !ok {
			http.Error(w, "невалидный тикет", http.StatusUnauthorized)
			return
		}

		// Спавн привязан к контексту запроса: разрыв WS отменяет ctx и локальный
		// шелл-процесс получает сигнал завершения даже без явного Terminate.
		shell, err := shells.Shell(r.Context(), sessionID, userID)
		if err != nil {
			log.Printf("termws: shell %s: %v", sessionID, err)
			http.Error(w, "шелл недоступен", http.StatusNotFound)
			return
		}

		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			terminateShell(shell, sessionID)
			return
		}
		defer conn.CloseNow()
		// Шелл одноразов: разорванное подключение завершает его процесс, повторное
		// открытие панели спавнит новый.
		defer terminateShell(shell, sessionID)

		serve(r.Context(), conn, shell)
	})
}

// terminateShell завершает процесс шелла с собственным бюджетом времени: контекст
// запроса к этому моменту уже отменён и для teardown непригоден.
func terminateShell(shell Shell, sessionID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := shell.Terminate(ctx); err != nil {
		log.Printf("termws: shell terminate %s: %v", sessionID, err)
	}
}

// serve связывает WebSocket с псевдотерминалом до разрыва любой из сторон.
//
// Поток pty→WS копируется в отдельной горутине через адаптер net.Conn (бинарные
// фреймы). Поток WS→pty читается в текущей горутине покадрово как JSON. Завершение
// любого направления отменяет общий ctx и закрывает соединение.
func serve(ctx context.Context, conn *websocket.Conn, handle Terminal) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// pty→WS: оборачиваем WS в net.Conn с бинарным типом сообщений. Сначала отдаём
	// накопленный хвост вывода (восстановление экрана при переподключении/перезагрузке
	// страницы), затем копируем живой вывод pty. io.Copy завершится, когда pty
	// закроется или WS оборвётся.
	go func() {
		defer cancel()
		netConn := websocket.NetConn(ctx, conn, websocket.MessageBinary)
		if hist := handle.History(); len(hist) > 0 {
			if _, err := netConn.Write(hist); err != nil {
				return
			}
		}
		_, _ = io.Copy(netConn, handle)
	}()

	// WS→pty: покадровое чтение управляющих JSON-сообщений.
	for {
		typ, data, err := conn.Read(ctx)
		if err != nil {
			return
		}
		// От клиента ожидаются только текстовые управляющие кадры; бинарные игнорируем.
		if typ != websocket.MessageText {
			continue
		}

		var msg clientMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			continue
		}

		switch msg.Type {
		case "input":
			if _, err := io.WriteString(handle, msg.Data); err != nil {
				return
			}
		case "resize":
			// Ошибку resize не считаем фатальной: терминал продолжит работать со
			// старым размером, обрывать сессию из-за этого нецелесообразно.
			_ = handle.Resize(msg.Cols, msg.Rows)
		default:
			// Неизвестные типы сообщений игнорируются для совместимости вперёд.
		}
	}
}
