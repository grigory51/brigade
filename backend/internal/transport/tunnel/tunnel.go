package tunnel

import (
	"context"
	"io"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/coder/websocket"

	"github.com/grigory51/brigade/backend/internal/preview"
)

type TicketRedeemer interface {
	RedeemScoped(token, sessionID, scope string) (userID string, ok bool)
}

// Handler проксирует один WebSocket в один TCP stream внутри среды сессии.
func Handler(tickets TicketRedeemer, resolver *preview.Resolver) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sessionID := r.PathValue("sessionId")
		if _, ok := tickets.RedeemScoped(r.URL.Query().Get("ticket"), sessionID, "tunnel"); !ok {
			http.Error(w, "invalid ticket", http.StatusUnauthorized)
			return
		}
		port, err := strconv.Atoi(r.PathValue("port"))
		if err != nil || port < 1 || port > 65535 {
			http.Error(w, "invalid port", http.StatusBadRequest)
			return
		}
		upstream, err := resolver.Resolve(r.Context(), sessionID, port)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		conn, err := net.DialTimeout("tcp", upstream.Host, 10*time.Second)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer conn.Close()
		ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: []string{"*"}})
		if err != nil {
			return
		}
		defer ws.CloseNow()

		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()
		errCh := make(chan error, 2)
		go func() {
			for {
				typ, data, err := ws.Read(ctx)
				if err != nil {
					errCh <- err
					return
				}
				if typ != websocket.MessageBinary {
					continue
				}
				if _, err := conn.Write(data); err != nil {
					errCh <- err
					return
				}
			}
		}()
		go func() {
			buf := make([]byte, 32<<10)
			for {
				n, err := conn.Read(buf)
				if n > 0 {
					if writeErr := ws.Write(ctx, websocket.MessageBinary, buf[:n]); writeErr != nil {
						errCh <- writeErr
						return
					}
				}
				if err != nil {
					errCh <- err
					return
				}
			}
		}()
		if err := <-errCh; err != nil && err != io.EOF {
			_ = ws.Close(websocket.StatusInternalError, "tunnel closed")
		}
		cancel()
	})
}
