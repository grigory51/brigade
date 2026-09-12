package session

import (
	"context"
	"errors"

	v1 "github.com/grigory51/brigade/backend/gen/go/brigade/v1"
	"github.com/grigory51/brigade/backend/internal/browser"
	"github.com/grigory51/brigade/backend/internal/store"
)

const browserInstructions = `Brigade provides built-in MCP tools browser and browser_handoff (search for deferred tools if needed). When curl or an HTTP fetch is blocked by a CAPTCHA, JavaScript challenge, or login, open the original HTTP(S) URL using browser with action=open and the same explicitly configured proxy. Never render downloaded challenge HTML under Brigade's origin. If a human must solve a CAPTCHA, sign in, enter a one-time code, or confirm an action, call browser_handoff with a short reason and end your turn. Do not solve CAPTCHA challenges automatically, ask for credentials in chat, read the browser while the user controls it, or poll for completion. The user returns with a message when finished; then continue through browser/read in the SAME browser context, not curl. Authentication remains in that session browser. If the user cancels, do not repeat the protected operation.`

func (r *Registry) BrowserInteract(ctx context.Context, userID string, req *v1.BrowserRequest) (*v1.BrowserResponse, error) {
	sess, err := r.Get(ctx, req.SessionId, userID)
	if err != nil {
		return nil, err
	}
	client, ok := r.ACPClient(req.SessionId, userID)
	if !ok {
		return nil, errors.New("Браузер недоступен: сессия остановлена")
	}
	if sess.Mode == store.SessionModeDocker {
		remote, ok := client.(interface {
			BrowserInteract(context.Context, *v1.BrowserRequest) (*v1.BrowserResponse, error)
		})
		if !ok {
			return nil, errors.New("Обновите runtime сессии для управления браузером")
		}
		return remote.BrowserInteract(ctx, req)
	}
	return browser.Interact(ctx, sess.ID, req)
}
