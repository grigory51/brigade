// Package termws обслуживает WebSocket терминала CLI-режима.
//
// Эндпоинт апгрейдит HTTP-соединение до WebSocket, аутентифицирует клиента по
// одноразовому тикету (?ticket=) и связывает поток псевдотерминала агента
// (spawn.Handle) с WebSocket:
//
//   - сервер→клиент: raw-байты pty бинарными фреймами (io.Copy handle→WS);
//   - клиент→сервер: JSON-сообщения {type:"input",data} | {type:"resize",cols,rows}.
//
// Resize прокидывается в handle.Resize (pty.Setsize / docker ContainerResize). Сам
// агент спавнится не здесь, а реестром живых сессий; термина получает уже готовый
// Handle по sessionID через HandleProvider.
package termws

import (
	"context"
	"encoding/json"
	"io"
	"net/http"

	"github.com/coder/websocket"

	"github.com/grigory51/brigade/backend/internal/spawn"
)

// TicketRedeemer проверяет одноразовый WS-тикет, привязанный к session_id, и
// возвращает id пользователя. Удовлетворяется auth.TicketStore.
type TicketRedeemer interface {
	Redeem(token, sessionID string) (userID string, ok bool)
}

// HandleProvider отдаёт активный Handle псевдотерминала по идентификатору сессии.
// Удовлетворяется реестром живых сессий (internal/session, шаг 3). До его готовности
// транспорт компилируется и тестируется против любой реализации этого интерфейса.
type HandleProvider interface {
	// Handle возвращает Handle сессии и принадлежащего ей пользователя. ok=false,
	// если сессия неизвестна, мертва или принадлежит другому пользователю.
	Handle(sessionID, userID string) (h spawn.Handle, ok bool)
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

		handle, ok := provider.Handle(sessionID, userID)
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

// serve связывает WebSocket с pty-хендлом до разрыва любой из сторон.
//
// Поток pty→WS копируется в отдельной горутине через адаптер net.Conn (бинарные
// фреймы). Поток WS→pty читается в текущей горутине покадрово как JSON. Завершение
// любого направления отменяет общий ctx и закрывает соединение.
func serve(ctx context.Context, conn *websocket.Conn, handle spawn.Handle) {
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
