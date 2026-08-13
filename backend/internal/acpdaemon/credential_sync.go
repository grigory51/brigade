package acpdaemon

import (
	"bytes"
	"context"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/fsnotify/fsnotify"

	v1 "github.com/grigory51/brigade/backend/gen/go/brigade/v1"
	"github.com/grigory51/brigade/backend/gen/go/brigade/v1/brigadev1connect"
)

type credentialSync struct {
	mu        sync.Mutex
	path      string
	sessionID string
	token     string
	client    brigadev1connect.AgentBridgeServiceClient
	last      []byte
	watcher   *fsnotify.Watcher
}

func newCredentialSync(path string, env []string) *credentialSync {
	values := make(map[string]string, len(env))
	for _, item := range env {
		if key, value, ok := strings.Cut(item, "="); ok {
			values[key] = value
		}
	}
	if path == "" || values["BRIGADE_API_URL"] == "" || values["BRIGADE_SESSION_TOKEN"] == "" || values["BRIGADE_SESSION_ID"] == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		log.Printf("acpdaemon: read agent credential: %v", err)
		return nil
	}
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Printf("acpdaemon: watch agent credential: %v", err)
		return nil
	}
	path = filepath.Clean(path)
	if err := watcher.Add(filepath.Dir(path)); err != nil {
		_ = watcher.Close()
		log.Printf("acpdaemon: watch agent credential directory: %v", err)
		return nil
	}
	s := &credentialSync{
		path: path, sessionID: values["BRIGADE_SESSION_ID"], token: values["BRIGADE_SESSION_TOKEN"],
		client: brigadev1connect.NewAgentBridgeServiceClient(http.DefaultClient, values["BRIGADE_API_URL"]),
		last:   append([]byte(nil), data...), watcher: watcher,
	}
	go s.watch()
	return s
}

func (s *credentialSync) watch() {
	for {
		select {
		case event, ok := <-s.watcher.Events:
			if !ok {
				return
			}
			if filepath.Clean(event.Name) != s.path || event.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Rename) == 0 {
				continue
			}
			if err := s.sync(context.Background(), false); err != nil {
				log.Printf("acpdaemon: sync agent credential: %v", err)
			}
		case err, ok := <-s.watcher.Errors:
			if !ok {
				return
			}
			log.Printf("acpdaemon: watch agent credential: %v", err)
		}
	}
}

func (s *credentialSync) sync(ctx context.Context, force bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, err := os.ReadFile(s.path)
	if err != nil {
		return err
	}
	if !force && bytes.Equal(current, s.last) {
		return nil
	}
	req := connect.NewRequest(&v1.SyncCredentialRequest{SessionId: s.sessionID, Previous: s.last, Current: current})
	req.Header().Set("Authorization", "Bearer "+s.token)
	callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	resp, err := s.client.SyncCredential(callCtx, req)
	if err != nil {
		return err
	}
	if !bytes.Equal(resp.Msg.Current, current) {
		if err := writeCredential(s.path, resp.Msg.Current); err != nil {
			return err
		}
	}
	s.last = append(s.last[:0], resp.Msg.Current...)
	return nil
}

func writeCredential(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".credential-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

func (s *credentialSync) close() {
	if s != nil {
		_ = s.watcher.Close()
	}
}
