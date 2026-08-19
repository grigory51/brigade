package connectsvc

import (
	"context"

	"connectrpc.com/connect"

	v1 "github.com/grigory51/brigade/backend/gen/go/brigade/v1"
	"github.com/grigory51/brigade/backend/internal/plugin"
	"github.com/grigory51/brigade/backend/internal/store"
)

type pluginCaller interface {
	PluginMCP(context.Context, string, string, string, []byte) ([]byte, error)
}

type PluginService struct {
	store   *store.Store
	session pluginCaller
}

func NewPluginService(st *store.Store, sessions pluginCaller) *PluginService {
	return &PluginService{store: st, session: sessions}
}

func (s *PluginService) List(ctx context.Context, _ *connect.Request[v1.ListPluginsRequest]) (*connect.Response[v1.ListPluginsResponse], error) {
	if _, err := requireUser(ctx); err != nil {
		return nil, err
	}
	installed, err := s.store.ListPlugins(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	response := &v1.ListPluginsResponse{}
	for _, item := range installed {
		mapped, err := mapPlugin(item)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		response.Plugins = append(response.Plugins, mapped)
	}
	return connect.NewResponse(response), nil
}

func (s *PluginService) Get(ctx context.Context, req *connect.Request[v1.GetPluginRequest]) (*connect.Response[v1.Plugin], error) {
	userID, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	sess, err := s.store.GetSession(ctx, req.Msg.SessionId)
	if err != nil || sess.UserID != userID || sess.ExperienceID == "" {
		return nil, connect.NewError(connect.CodeNotFound, store.ErrNotFound)
	}
	installed, err := s.store.GetPlugin(ctx, sess.ExperienceID, sess.ExperienceVersion)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, err)
	}
	mapped, err := mapPlugin(installed)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(mapped), nil
}

func mapPlugin(item store.Plugin) (*v1.Plugin, error) {
	manifest, err := plugin.ParseManifest([]byte(item.ManifestJSON))
	if err != nil {
		return nil, err
	}
	result := &v1.Plugin{
		Id: item.ID, Name: item.Name, Description: manifest.Description, Version: item.Version,
		Icon: manifest.Icon, EntryTool: manifest.Meta.Brigade.Experience.EntryTool,
	}
	if cover := manifest.Meta.Brigade.Experience.Cover; cover != "" {
		data, mimeType, err := plugin.ReadCover(item.BundlePath, manifest)
		if err != nil {
			return nil, err
		}
		result.Cover = data
		result.CoverMimeType = mimeType
	}
	return result, nil
}

func (s *PluginService) MCP(ctx context.Context, req *connect.Request[v1.PluginMCPRequest]) (*connect.Response[v1.PluginMCPResponse], error) {
	userID, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	result, err := s.session.PluginMCP(ctx, req.Msg.SessionId, userID, req.Msg.Method, req.Msg.ParamsJson)
	if err != nil {
		return nil, sessionError(err)
	}
	return connect.NewResponse(&v1.PluginMCPResponse{ResultJson: result}), nil
}
