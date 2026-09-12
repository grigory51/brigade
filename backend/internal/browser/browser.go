// Package browser connects the session's private browser broker to the authenticated UI.
package browser

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"syscall"
	"time"

	v1 "github.com/grigory51/brigade/backend/gen/go/brigade/v1"
	"google.golang.org/protobuf/encoding/protojson"
)

func SocketPath(sessionID string) string {
	hash := sha256.Sum256([]byte(sessionID))
	return fmt.Sprintf("/tmp/brigade-browser-%d-%x.sock", os.Getuid(), hash[:12])
}

func Interact(ctx context.Context, sessionID string, req *v1.BrowserRequest) (*v1.BrowserResponse, error) {
	actions := map[v1.BrowserAction]string{
		v1.BrowserAction_BROWSER_ACTION_STATUS: "status", v1.BrowserAction_BROWSER_ACTION_FRAME: "frame",
		v1.BrowserAction_BROWSER_ACTION_INPUT: "input", v1.BrowserAction_BROWSER_ACTION_RESUME: "resume",
		v1.BrowserAction_BROWSER_ACTION_CANCEL: "cancel",
	}
	action, ok := actions[req.Action]
	if !ok || req.RequestId == "" || len(req.Text) > 64000 {
		return nil, errors.New("browser: invalid request")
	}
	body, err := json.Marshal(map[string]any{"action": action, "requestId": req.RequestId, "input": req.Input, "x": req.X, "y": req.Y, "text": req.Text})
	if err != nil {
		return nil, err
	}
	data, err := call(ctx, sessionID, "user", body)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ECONNREFUSED) {
			return &v1.BrowserResponse{RequestId: req.RequestId, State: "expired"}, nil
		}
		return nil, err
	}
	response := new(v1.BrowserResponse)
	if err := protojson.Unmarshal(data, response); err != nil {
		return nil, fmt.Errorf("browser: invalid response: %w", err)
	}
	return response, nil
}

func Close(sessionID string) {
	if sessionID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, _ = call(ctx, sessionID, "close", []byte(`{}`))
}

func Debug(ctx context.Context, sessionID string) (*v1.BrowserDebugResponse, error) {
	data, err := call(ctx, sessionID, "debug", []byte(`{}`))
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ECONNREFUSED) {
		return &v1.BrowserDebugResponse{State: "not_started"}, nil
	}
	if err != nil {
		return nil, err
	}
	response := new(v1.BrowserDebugResponse)
	if err := protojson.Unmarshal(data, response); err != nil {
		return nil, fmt.Errorf("browser: invalid debug response: %w", err)
	}
	return response, nil
}

func call(ctx context.Context, sessionID, role string, body []byte) ([]byte, error) {
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", SocketPath(sessionID))
	}}
	defer transport.CloseIdleConnections()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://browser/"+role, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Transport: transport, Timeout: 40 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		var message struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(data, &message)
		return nil, fmt.Errorf("browser: %s", message.Error)
	}
	return data, nil
}
