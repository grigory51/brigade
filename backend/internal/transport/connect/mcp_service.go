package connectsvc

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	v1 "github.com/grigory51/brigade/backend/gen/go/brigade/v1"
	"github.com/grigory51/brigade/backend/internal/mcp"
	"github.com/grigory51/brigade/backend/internal/store"
)

// McpService реализует brigade.v1.McpService — персональные MCP-серверы пользователя и
// vault секретов для них. Логики сверх CRUD здесь нет: валидация конфига — в internal/mcp,
// шифрование значений секретов — в store; набор для конкретной сессии собирает реестр.
type McpService struct {
	store *store.Store
}

// NewMcpService собирает реализацию McpService.
func NewMcpService(st *store.Store) *McpService {
	return &McpService{store: st}
}

// ListServers возвращает MCP-серверы пользователя (значений секретов в конфигах нет — там
// ссылки вида ${secret.NAME}).
func (s *McpService) ListServers(ctx context.Context, _ *connect.Request[v1.ListMcpServersRequest]) (*connect.Response[v1.ListMcpServersResponse], error) {
	userID, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	servers, err := s.store.ListMcpServers(ctx, userID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	out := make([]*v1.McpServer, 0, len(servers))
	for _, srv := range servers {
		out = append(out, mcpServerToProto(srv))
	}
	return connect.NewResponse(&v1.ListMcpServersResponse{Servers: out}), nil
}

// CreateServer заводит новый сервер пользователя.
func (s *McpService) CreateServer(ctx context.Context, req *connect.Request[v1.CreateMcpServerRequest]) (*connect.Response[v1.McpServerResponse], error) {
	userID, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	srv, err := mcpServerFromProto(req.Msg.Server, userID)
	if err != nil {
		return nil, err
	}
	srv.ID = uuid.NewString()
	srv.CreatedAt = time.Now()
	if err := s.store.CreateMcpServer(ctx, srv); err != nil {
		return nil, mcpStoreError(err, srv.Name)
	}
	return connect.NewResponse(&v1.McpServerResponse{Server: mcpServerToProto(srv)}), nil
}

// UpdateServer перезаписывает конфиг существующего сервера.
func (s *McpService) UpdateServer(ctx context.Context, req *connect.Request[v1.UpdateMcpServerRequest]) (*connect.Response[v1.McpServerResponse], error) {
	userID, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	srv, err := mcpServerFromProto(req.Msg.Server, userID)
	if err != nil {
		return nil, err
	}
	if srv.ID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("не задан идентификатор сервера"))
	}
	if err := s.store.UpdateMcpServer(ctx, srv); err != nil {
		return nil, mcpStoreError(err, srv.Name)
	}
	return connect.NewResponse(&v1.McpServerResponse{Server: mcpServerToProto(srv)}), nil
}

// DeleteServer удаляет сервер пользователя.
func (s *McpService) DeleteServer(ctx context.Context, req *connect.Request[v1.DeleteMcpServerRequest]) (*connect.Response[v1.Empty], error) {
	userID, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.store.DeleteMcpServer(ctx, req.Msg.Id, userID); err != nil {
		return nil, mcpStoreError(err, "")
	}
	return connect.NewResponse(&v1.Empty{}), nil
}

// ListSecrets возвращает имена секретов пользователя. Значения не отдаются никогда.
func (s *McpService) ListSecrets(ctx context.Context, _ *connect.Request[v1.ListSecretsRequest]) (*connect.Response[v1.ListSecretsResponse], error) {
	userID, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	secrets, err := s.store.ListSecrets(ctx, userID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	out := make([]*v1.McpSecret, 0, len(secrets))
	for _, sec := range secrets {
		out = append(out, &v1.McpSecret{Name: sec.Name, UpdatedAt: sec.UpdatedAt.Unix()})
	}
	return connect.NewResponse(&v1.ListSecretsResponse{Secrets: out}), nil
}

// SetSecret задаёт или заменяет значение секрета.
func (s *McpService) SetSecret(ctx context.Context, req *connect.Request[v1.SetSecretRequest]) (*connect.Response[v1.Empty], error) {
	userID, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	if !mcp.SecretName.MatchString(req.Msg.Name) {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			errors.New("имя секрета: допустимы латиница, цифры и подчёркивание (до 64 символов)"))
	}
	if req.Msg.Value == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("пустое значение секрета"))
	}
	if err := s.store.SetSecret(ctx, userID, req.Msg.Name, req.Msg.Value); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&v1.Empty{}), nil
}

// DeleteSecret удаляет секрет пользователя.
func (s *McpService) DeleteSecret(ctx context.Context, req *connect.Request[v1.DeleteSecretRequest]) (*connect.Response[v1.Empty], error) {
	userID, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.store.DeleteSecret(ctx, userID, req.Msg.Name); err != nil {
		return nil, mcpStoreError(err, "")
	}
	return connect.NewResponse(&v1.Empty{}), nil
}

func mcpServerToProto(srv store.McpServer) *v1.McpServer {
	return &v1.McpServer{
		Id:        srv.ID,
		Name:      srv.Name,
		Transport: transportToProto(srv.Transport),
		Command:   srv.Command,
		Args:      srv.Args,
		Env:       kvToProto(srv.Env),
		Url:       srv.URL,
		Headers:   kvToProto(srv.Headers),
	}
}

// mcpServerFromProto переводит запрос в модель store и валидирует её.
func mcpServerFromProto(msg *v1.McpServer, userID string) (store.McpServer, error) {
	if msg == nil {
		return store.McpServer{}, connect.NewError(connect.CodeInvalidArgument, errors.New("пустой конфиг сервера"))
	}
	srv := store.McpServer{
		ID:        msg.Id,
		UserID:    userID,
		Name:      strings.TrimSpace(msg.Name),
		Transport: transportFromProto(msg.Transport),
		Command:   strings.TrimSpace(msg.Command),
		Args:      msg.Args,
		Env:       kvFromProto(msg.Env),
		URL:       strings.TrimSpace(msg.Url),
		Headers:   kvFromProto(msg.Headers),
	}
	if err := mcp.Validate(srv); err != nil {
		return store.McpServer{}, connect.NewError(connect.CodeInvalidArgument, err)
	}
	return srv, nil
}

func transportToProto(t store.McpTransport) v1.McpTransport {
	switch t {
	case store.McpTransportStdio:
		return v1.McpTransport_MCP_TRANSPORT_STDIO
	case store.McpTransportHTTP:
		return v1.McpTransport_MCP_TRANSPORT_HTTP
	case store.McpTransportSSE:
		return v1.McpTransport_MCP_TRANSPORT_SSE
	default:
		return v1.McpTransport_MCP_TRANSPORT_UNSPECIFIED
	}
}

func transportFromProto(t v1.McpTransport) store.McpTransport {
	switch t {
	case v1.McpTransport_MCP_TRANSPORT_STDIO:
		return store.McpTransportStdio
	case v1.McpTransport_MCP_TRANSPORT_HTTP:
		return store.McpTransportHTTP
	case v1.McpTransport_MCP_TRANSPORT_SSE:
		return store.McpTransportSSE
	default:
		return ""
	}
}

func kvToProto(pairs []store.McpKeyValue) []*v1.McpKeyValue {
	out := make([]*v1.McpKeyValue, 0, len(pairs))
	for _, kv := range pairs {
		out = append(out, &v1.McpKeyValue{Name: kv.Name, Value: kv.Value})
	}
	return out
}

func kvFromProto(pairs []*v1.McpKeyValue) []store.McpKeyValue {
	out := make([]store.McpKeyValue, 0, len(pairs))
	for _, kv := range pairs {
		if kv == nil {
			continue
		}
		out = append(out, store.McpKeyValue{Name: strings.TrimSpace(kv.Name), Value: kv.Value})
	}
	return out
}

// mcpStoreError переводит ошибки хранилища в коды Connect: отсутствие записи — NotFound,
// конфликт имени (уникальный индекс) — AlreadyExists.
func mcpStoreError(err error, name string) error {
	if errors.Is(err, store.ErrNotFound) {
		return connect.NewError(connect.CodeNotFound, err)
	}
	if strings.Contains(err.Error(), "UNIQUE constraint failed") {
		return connect.NewError(connect.CodeAlreadyExists, fmt.Errorf("сервер с именем %q уже есть", name))
	}
	return connect.NewError(connect.CodeInternal, err)
}
