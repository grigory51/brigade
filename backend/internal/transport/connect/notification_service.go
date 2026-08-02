package connectsvc

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	v1 "github.com/grigory51/brigade/backend/gen/go/brigade/v1"
	"github.com/grigory51/brigade/backend/internal/auth"
	"github.com/grigory51/brigade/backend/internal/store"
)

type NotificationTester interface {
	Test(ctx context.Context, userID, id string) error
}

type NotificationService struct {
	store  *store.Store
	tester NotificationTester
}

func NewNotificationService(store *store.Store, tester NotificationTester) *NotificationService {
	return &NotificationService{store: store, tester: tester}
}

func (s *NotificationService) ListNotificationBackends(ctx context.Context, _ *connect.Request[v1.Empty]) (*connect.Response[v1.ListNotificationBackendsResponse], error) {
	user, ok := auth.UserFromContext(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("auth required"))
	}
	backends, err := s.store.ListNotificationBackends(ctx, user.ID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	out := make([]*v1.NotificationBackend, 0, len(backends))
	for _, backend := range backends {
		out = append(out, notificationBackendToProto(backend))
	}
	return connect.NewResponse(&v1.ListNotificationBackendsResponse{Backends: out}), nil
}

func (s *NotificationService) SaveNotificationBackend(ctx context.Context, req *connect.Request[v1.SaveNotificationBackendRequest]) (*connect.Response[v1.NotificationBackend], error) {
	user, ok := auth.UserFromContext(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("auth required"))
	}
	in := req.Msg.Backend
	if in == nil || in.Kind != "ntfy" || in.Ntfy == nil || strings.TrimSpace(in.Ntfy.Topic) == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("ntfy topic required"))
	}
	id := in.Id
	if id == "" {
		id = uuid.NewString()
	} else {
		backends, err := s.store.ListNotificationBackends(ctx, user.ID)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		owned := false
		for _, backend := range backends {
			owned = owned || backend.ID == id
		}
		if !owned {
			return nil, connect.NewError(connect.CodeNotFound, errors.New("notification backend not found"))
		}
	}
	config, _ := json.Marshal(map[string]string{
		"server": strings.TrimSpace(in.Ntfy.Server),
		"topic":  strings.TrimSpace(in.Ntfy.Topic),
	})
	backend := store.NotificationBackend{
		ID: id, UserID: user.ID, Kind: in.Kind, Name: strings.TrimSpace(in.Name),
		Config: string(config), Secret: strings.TrimSpace(req.Msg.Secret),
		Events: strings.Join(in.Events, ","),
	}
	if backend.Name == "" {
		backend.Name = "ntfy"
	}
	if err := s.store.SaveNotificationBackend(ctx, backend); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	backends, err := s.store.ListNotificationBackends(ctx, user.ID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	for _, saved := range backends {
		if saved.ID == id {
			return connect.NewResponse(notificationBackendToProto(saved)), nil
		}
	}
	return nil, connect.NewError(connect.CodeInternal, errors.New("saved backend not found"))
}

func (s *NotificationService) DeleteNotificationBackend(ctx context.Context, req *connect.Request[v1.NotificationBackendRequest]) (*connect.Response[v1.Empty], error) {
	user, ok := auth.UserFromContext(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("auth required"))
	}
	if err := s.store.DeleteNotificationBackend(ctx, user.ID, req.Msg.Id); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&v1.Empty{}), nil
}

func (s *NotificationService) TestNotificationBackend(ctx context.Context, req *connect.Request[v1.NotificationBackendRequest]) (*connect.Response[v1.Empty], error) {
	user, ok := auth.UserFromContext(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("auth required"))
	}
	if err := s.tester.Test(ctx, user.ID, req.Msg.Id); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	return connect.NewResponse(&v1.Empty{}), nil
}

func notificationBackendToProto(backend store.NotificationBackend) *v1.NotificationBackend {
	var config struct {
		Server string `json:"server"`
		Topic  string `json:"topic"`
	}
	_ = json.Unmarshal([]byte(backend.Config), &config)
	return &v1.NotificationBackend{
		Id: backend.ID, Kind: backend.Kind, Name: backend.Name,
		Events: splitEvents(backend.Events),
		Ntfy: &v1.NtfyNotificationConfig{
			Server: config.Server, Topic: config.Topic, TokenSet: backend.Secret != "",
		},
	}
}

func splitEvents(csv string) []string {
	if strings.TrimSpace(csv) == "" {
		return nil
	}
	return strings.Split(csv, ",")
}
