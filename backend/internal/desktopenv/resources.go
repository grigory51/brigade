package desktopenv

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/coder/websocket"
	"github.com/google/uuid"

	v1 "github.com/grigory51/brigade/backend/gen/go/brigade/v1"
	"github.com/grigory51/brigade/backend/gen/go/brigade/v1/brigadev1connect"
)

type PortForward struct {
	ID         string `json:"id"`
	SessionID  string `json:"session_id"`
	RemotePort int    `json:"remote_port"`
	LocalPort  int    `json:"local_port"`
}

type Mount struct {
	ID        string `json:"id"`
	SessionID string `json:"session_id"`
	Path      string `json:"path"`
}

type mountHandle interface{ Close() error }

func mountPath(environmentName, sessionName, sessionID string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	clean := func(value string) string {
		value = strings.TrimSpace(value)
		value = strings.Map(func(r rune) rune {
			if r == '/' || r == ':' || r == 0 {
				return '-'
			}
			return r
		}, value)
		if value == "" {
			return "session"
		}
		return value
	}
	shortID := sessionID
	if len(shortID) > 8 {
		shortID = shortID[:8]
	}
	return filepath.Join(home, "Brigade", clean(environmentName), clean(sessionName)+"--"+shortID), nil
}

func (m *Manager) AddMount(ctx context.Context, sessionID, sessionName string) (Mount, error) {
	m.mu.Lock()
	environment := m.findLocked(m.config.ActiveID)
	if environment == nil || environment.Kind != "remote" {
		m.mu.Unlock()
		return Mount{}, fmt.Errorf("mount доступен для remote-окружения")
	}
	for _, mount := range environment.Mounts {
		if mount.SessionID == sessionID {
			m.mu.Unlock()
			return mount, nil
		}
	}
	path, err := mountPath(environment.Name, sessionName, sessionID)
	if err != nil {
		m.mu.Unlock()
		return Mount{}, err
	}
	mount := Mount{ID: uuid.NewString(), SessionID: sessionID, Path: path}
	environment.Mounts = append(environment.Mounts, mount)
	if err := m.saveLocked(); err != nil {
		m.mu.Unlock()
		return Mount{}, err
	}
	environmentID := environment.ID
	m.mu.Unlock()
	handle, err := m.platformMount(ctx, environmentID, mount)
	if err != nil {
		m.mu.Lock()
		m.resourceErrors[mount.ID] = err.Error()
		m.mu.Unlock()
		return mount, err
	}
	m.mu.Lock()
	m.mountHandles[mount.ID] = handle
	delete(m.resourceErrors, mount.ID)
	m.mu.Unlock()
	return mount, nil
}

func (m *Manager) RemoveMount(id string) error {
	m.mu.Lock()
	handle := m.mountHandles[id]
	delete(m.mountHandles, id)
	for _, environment := range m.config.Environments {
		for index, mount := range environment.Mounts {
			if mount.ID == id {
				environment.Mounts = append(environment.Mounts[:index], environment.Mounts[index+1:]...)
				delete(m.resourceErrors, id)
				err := m.saveLocked()
				m.mu.Unlock()
				if handle != nil {
					err = errors.Join(err, handle.Close())
				}
				return err
			}
		}
	}
	m.mu.Unlock()
	return fmt.Errorf("mount не найден")
}

func (m *Manager) AddPortForward(ctx context.Context, sessionID string, remotePort, localPort int) (PortForward, error) {
	if remotePort < 1 || remotePort > 65535 || localPort < 0 || localPort > 65535 {
		return PortForward{}, fmt.Errorf("некорректный порт")
	}
	forward := PortForward{ID: uuid.NewString(), SessionID: sessionID, RemotePort: remotePort, LocalPort: localPort}
	listener, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(localPort))
	if err != nil {
		return PortForward{}, err
	}
	forward.LocalPort = listener.Addr().(*net.TCPAddr).Port
	m.mu.Lock()
	environment := m.findLocked(m.config.ActiveID)
	if environment == nil || environment.Kind != "remote" {
		m.mu.Unlock()
		listener.Close()
		return PortForward{}, fmt.Errorf("port forwarding доступен для remote-окружения")
	}
	environment.PortForwards = append(environment.PortForwards, forward)
	m.forwardListeners[forward.ID] = listener
	ctx = m.resourceContext
	if err := m.saveLocked(); err != nil {
		delete(m.forwardListeners, forward.ID)
		m.mu.Unlock()
		listener.Close()
		return PortForward{}, err
	}
	m.mu.Unlock()
	go m.serveForward(ctx, environment.ID, forward, listener)
	return forward, nil
}

func (m *Manager) RemovePortForward(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if listener := m.forwardListeners[id]; listener != nil {
		_ = listener.Close()
		delete(m.forwardListeners, id)
	}
	for _, environment := range m.config.Environments {
		for index, forward := range environment.PortForwards {
			if forward.ID == id {
				environment.PortForwards = append(environment.PortForwards[:index], environment.PortForwards[index+1:]...)
				delete(m.resourceErrors, id)
				return m.saveLocked()
			}
		}
	}
	return fmt.Errorf("проброс не найден")
}

func (m *Manager) serveForward(ctx context.Context, environmentID string, forward PortForward, listener net.Listener) {
	for {
		connection, err := listener.Accept()
		if err != nil {
			return
		}
		go func() {
			defer connection.Close()
			if err := m.forwardConnection(ctx, environmentID, forward, connection); err != nil {
				m.mu.Lock()
				m.resourceErrors[forward.ID] = err.Error()
				m.mu.Unlock()
			}
		}()
	}
}

func (m *Manager) forwardConnection(ctx context.Context, environmentID string, forward PortForward, local net.Conn) error {
	environment, token, err := m.tokenFor(ctx, environmentID)
	if err != nil {
		return err
	}
	client := brigadev1connect.NewSessionServiceClient(m.http, environment.BaseURL)
	request := connect.NewRequest(&v1.IssueStreamTicketRequest{SessionId: forward.SessionID, Scope: "tunnel"})
	request.Header().Set("Authorization", "Bearer "+token)
	ticket, err := client.IssueStreamTicket(ctx, request)
	if err != nil {
		return err
	}
	endpoint, _ := url.Parse(environment.BaseURL)
	if endpoint.Scheme == "https" {
		endpoint.Scheme = "wss"
	} else {
		endpoint.Scheme = "ws"
	}
	endpoint.Path = fmt.Sprintf("/ws/tunnel/%s/%d", forward.SessionID, forward.RemotePort)
	query := endpoint.Query()
	query.Set("ticket", ticket.Msg.Ticket)
	endpoint.RawQuery = query.Encode()
	ws, _, err := websocket.Dial(ctx, endpoint.String(), &websocket.DialOptions{HTTPClient: &http.Client{Timeout: 15 * time.Second}})
	if err != nil {
		return err
	}
	defer ws.CloseNow()
	remote := websocket.NetConn(ctx, ws, websocket.MessageBinary)
	defer remote.Close()
	errCh := make(chan error, 2)
	go func() { _, err := io.Copy(remote, local); errCh <- err }()
	go func() { _, err := io.Copy(local, remote); errCh <- err }()
	return <-errCh
}

func (m *Manager) detachResourcesLocked() ([]net.Listener, []mountHandle) {
	m.resourceCancel()
	m.resourceContext, m.resourceCancel = context.WithCancel(context.Background())
	listeners := make([]net.Listener, 0, len(m.forwardListeners))
	for id, listener := range m.forwardListeners {
		listeners = append(listeners, listener)
		delete(m.forwardListeners, id)
	}
	handles := make([]mountHandle, 0, len(m.mountHandles))
	for id, handle := range m.mountHandles {
		handles = append(handles, handle)
		delete(m.mountHandles, id)
	}
	return listeners, handles
}

func closeResources(listeners []net.Listener, handles []mountHandle) {
	for _, listener := range listeners {
		_ = listener.Close()
	}
	for _, handle := range handles {
		_ = handle.Close()
	}
}

func (m *Manager) Restore(ctx context.Context) {
	m.mu.Lock()
	environment := m.findLocked(m.config.ActiveID)
	if environment == nil || environment.Kind != "remote" {
		m.mu.Unlock()
		return
	}
	environmentID := environment.ID
	ctx = m.resourceContext
	forwards := append([]PortForward(nil), environment.PortForwards...)
	mounts := append([]Mount(nil), environment.Mounts...)
	m.mu.Unlock()
	for _, forward := range forwards {
		listener, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(forward.LocalPort))
		if err != nil {
			m.mu.Lock()
			m.resourceErrors[forward.ID] = err.Error()
			m.mu.Unlock()
			continue
		}
		m.mu.Lock()
		m.forwardListeners[forward.ID] = listener
		delete(m.resourceErrors, forward.ID)
		m.mu.Unlock()
		go m.serveForward(ctx, environmentID, forward, listener)
	}
	for _, mount := range mounts {
		handle, err := m.platformMount(ctx, environmentID, mount)
		m.mu.Lock()
		if err != nil {
			m.resourceErrors[mount.ID] = err.Error()
		} else {
			m.mountHandles[mount.ID] = handle
			delete(m.resourceErrors, mount.ID)
		}
		m.mu.Unlock()
	}
}

func (m *Manager) ResourceStatus(id string) (string, string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.resourceErrors[id]; err != "" {
		return "error", err
	}
	if m.forwardListeners[id] != nil {
		return "connected", ""
	}
	if m.mountHandles[id] != nil {
		return "connected", ""
	}
	return "stopped", ""
}
