package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"testing"

	"github.com/google/uuid"
	v1 "github.com/grigory51/brigade/backend/gen/go/brigade/v1"
)

func TestSocketPathMatchesNode(t *testing.T) {
	if got, want := SocketPath("abc"), fmt.Sprintf("/tmp/brigade-browser-%d-ba7816bf8f01cfea414140de.sock", os.Getuid()); got != want {
		t.Fatalf("socket: got %s want %s", got, want)
	}
}

func TestDebugDoesNotStartBrowser(t *testing.T) {
	resp, err := Debug(context.Background(), uuid.NewString())
	if err != nil || resp.State != "not_started" {
		t.Fatalf("debug: %v %v", resp, err)
	}
}

func TestDebugReadsBrowserDiagnostics(t *testing.T) {
	id := uuid.NewString()
	listener, err := net.Listen("unix", SocketPath(id))
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/debug" {
			t.Errorf("unexpected endpoint %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"state":"human","browsersPath":"/opt/brigade-browser","proxy":"http://127.0.0.1:3129","network":[{"origin":"https://example.org","resourceType":"document","status":403},{"origin":"https://example.org","error":"net::ERR_PROXY_CONNECTION_FAILED"}]}`))
	})}
	go server.Serve(listener)
	t.Cleanup(func() { _ = server.Close() })
	resp, err := Debug(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Proxy != "http://127.0.0.1:3129" || resp.BrowsersPath != "/opt/brigade-browser" || len(resp.Network) != 2 || resp.Network[0].Status != 403 || resp.Network[1].Error != "net::ERR_PROXY_CONNECTION_FAILED" {
		t.Fatalf("unexpected diagnostics: %v", resp)
	}
}

func TestInteractAndExpired(t *testing.T) {
	id := uuid.NewString()
	req := &v1.BrowserRequest{RequestId: "request", Action: v1.BrowserAction_BROWSER_ACTION_FRAME}
	resp, err := Interact(context.Background(), id, req)
	if err != nil || resp.State != "expired" {
		t.Fatalf("expired: %v %v", resp, err)
	}
	listener, err := net.Listen("unix", SocketPath(id))
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		if r.URL.Path != "/user" || payload["action"] != "frame" || payload["requestId"] != "request" {
			t.Errorf("unexpected request: %v %s", payload, r.URL)
		}
		_, _ = w.Write([]byte(`{"requestId":"request","state":"human","width":1024,"height":720,"image":"aGk="}`))
	})}
	go server.Serve(listener)
	t.Cleanup(func() { _ = server.Close() })
	resp, err = Interact(context.Background(), id, req)
	if err != nil || resp.State != "human" || string(resp.Image) != "hi" {
		t.Fatalf("frame: %v %v", resp, err)
	}
	if _, err := Interact(context.Background(), id, &v1.BrowserRequest{RequestId: "request"}); err == nil {
		t.Fatal("unspecified action accepted")
	}
}
