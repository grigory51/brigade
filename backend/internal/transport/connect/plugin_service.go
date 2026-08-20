package connectsvc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

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
	manager *plugin.Manager
	session pluginCaller
	target  string
}

func NewPluginService(st *store.Store, manager *plugin.Manager, sessions pluginCaller, target string) *PluginService {
	return &PluginService{store: st, manager: manager, session: sessions, target: target}
}

func (s *PluginService) List(ctx context.Context, _ *connect.Request[v1.ListPluginsRequest]) (*connect.Response[v1.ListPluginsResponse], error) {
	userID, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	installed, err := s.store.ListPlugins(ctx, userID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	groups := map[string][]store.Plugin{}
	var order []string
	for _, item := range installed {
		if _, ok := groups[item.ID]; !ok {
			order = append(order, item.ID)
		}
		if len(groups[item.ID]) > 0 && groups[item.ID][0].OwnerID == userID && item.OwnerID == "" {
			continue
		}
		groups[item.ID] = append(groups[item.ID], item)
	}
	response := &v1.ListPluginsResponse{RequiredTarget: s.target}
	for _, id := range order {
		mapped, err := s.mapPlugin(ctx, userID, groups[id])
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		response.Plugins = append(response.Plugins, mapped)
	}
	return connect.NewResponse(response), nil
}

func (s *PluginService) Install(ctx context.Context, req *connect.Request[v1.InstallPluginRequest]) (*connect.Response[v1.Plugin], error) {
	userID, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(req.Msg.Url, "https://") {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("MCPB URL должен использовать HTTPS"))
	}
	installed, err := s.manager.InstallFor(ctx, userID, req.Msg.Url)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	return s.pluginByID(ctx, userID, installed.ID)
}

func (s *PluginService) Update(ctx context.Context, req *connect.Request[v1.UpdatePluginRequest]) (*connect.Response[v1.Plugin], error) {
	userID, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := s.manager.UpdateFor(ctx, userID, req.Msg.Id); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	return s.pluginByID(ctx, userID, req.Msg.Id)
}

func (s *PluginService) Delete(ctx context.Context, req *connect.Request[v1.DeletePluginRequest]) (*connect.Response[v1.Empty], error) {
	userID, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	installed, err := s.store.GetPlugin(ctx, userID, req.Msg.Id, "", "")
	if err != nil || installed.OwnerID != userID {
		return nil, connect.NewError(connect.CodeNotFound, store.ErrNotFound)
	}
	manifest, err := plugin.ParseManifest([]byte(installed.ManifestJSON))
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if err := s.manager.RemoveFor(ctx, userID, req.Msg.Id); err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	if err := s.store.DeletePluginConfig(ctx, userID, req.Msg.Id); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	for key, field := range manifest.UserConfig {
		if field.Sensitive {
			if err := s.store.DeleteSecret(ctx, userID, pluginSecretName(req.Msg.Id, key)); err != nil && !errors.Is(err, store.ErrNotFound) {
				return nil, connect.NewError(connect.CodeInternal, err)
			}
		}
	}
	return connect.NewResponse(&v1.Empty{}), nil
}

func (s *PluginService) SaveConfig(ctx context.Context, req *connect.Request[v1.SavePluginConfigRequest]) (*connect.Response[v1.Plugin], error) {
	userID, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	installed, err := s.store.GetPlugin(ctx, userID, req.Msg.Id, "", "")
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, err)
	}
	manifest, err := plugin.ParseManifest([]byte(installed.ManifestJSON))
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	var input map[string]any
	if err := json.Unmarshal(req.Msg.ValuesJson, &input); err != nil || input == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("неверный JSON конфигурации"))
	}
	existing, err := s.store.GetPluginConfig(ctx, userID, req.Msg.Id)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	var values map[string]any
	if err := json.Unmarshal([]byte(existing.ValuesJSON), &values); err != nil {
		values = map[string]any{}
	}
	if values == nil {
		values = map[string]any{}
	}
	for key, field := range manifest.UserConfig {
		value, provided := input[key]
		if field.Sensitive {
			if text, ok := value.(string); provided && ok && text != "" {
				name := pluginSecretName(req.Msg.Id, key)
				if err := s.store.SetSecret(ctx, userID, name, text); err != nil {
					return nil, connect.NewError(connect.CodeInternal, err)
				}
				values[key] = "${secret." + name + "}"
			}
			continue
		}
		if provided {
			values[key] = value
		}
	}
	values, err = manifest.ResolveConfig(values, "")
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	raw, _ := json.Marshal(values)
	if err := s.store.PutPluginConfig(ctx, store.PluginConfig{UserID: userID, PluginID: req.Msg.Id, ValuesJSON: string(raw)}); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return s.pluginByID(ctx, userID, req.Msg.Id)
}

func pluginSecretName(pluginID, key string) string {
	hash := sha256.Sum256([]byte(pluginID))
	name := "PLUGIN_" + strings.ToUpper(hex.EncodeToString(hash[:6])) + "_" + strings.ToUpper(strings.ReplaceAll(key, "-", "_"))
	if len(name) > 64 {
		name = name[:64]
	}
	return name
}

func (s *PluginService) pluginByID(ctx context.Context, userID, id string) (*connect.Response[v1.Plugin], error) {
	installed, err := s.store.ListPlugins(ctx, userID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	var variants []store.Plugin
	for _, item := range installed {
		if item.ID == id && (len(variants) == 0 || variants[0].OwnerID == item.OwnerID) {
			variants = append(variants, item)
		}
	}
	if len(variants) == 0 {
		return nil, connect.NewError(connect.CodeNotFound, store.ErrNotFound)
	}
	mapped, err := s.mapPlugin(ctx, userID, variants)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(mapped), nil
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
	installed, err := s.store.GetPlugin(ctx, userID, sess.ExperienceID, sess.ExperienceVersion, plugin.RuntimeTarget(sess.Mode == store.SessionModeDocker))
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, err)
	}
	mapped, err := s.mapPlugin(ctx, userID, []store.Plugin{installed})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(mapped), nil
}

func (s *PluginService) mapPlugin(ctx context.Context, userID string, variants []store.Plugin) (*v1.Plugin, error) {
	item := variants[0]
	compatible := false
	hasBundle := false
	for _, candidate := range variants {
		candidateManifest, err := plugin.ParseManifest([]byte(candidate.ManifestJSON))
		if err != nil {
			return nil, err
		}
		if _, err := os.Stat(filepath.Join(candidate.BundlePath, filepath.FromSlash(candidateManifest.Server.EntryPoint))); err != nil {
			continue
		}
		if !hasBundle {
			item = candidate
			hasBundle = true
		}
		if targetMatches(candidate.Target, s.target) && plugin.SupportsTarget(candidateManifest, s.target) {
			item = candidate
			compatible = true
			break
		}
	}
	manifest, err := plugin.ParseManifest([]byte(item.ManifestJSON))
	if err != nil {
		return nil, err
	}
	result := &v1.Plugin{Id: item.ID, Name: item.Name, Description: manifest.Description, Version: item.Version,
		Icon: manifest.Icon, EntryTool: manifest.Meta.Brigade.Experience.EntryTool, System: item.OwnerID == "", Compatible: compatible, Configured: true}
	for _, variant := range variants {
		result.Variants = append(result.Variants, &v1.PluginVariant{Version: variant.Version, Target: variant.Target, Source: variant.Source, InstalledAt: variant.InstalledAt.Unix()})
	}
	if hasBundle && manifest.Meta.Brigade.Experience.Cover != "" {
		result.Cover, result.CoverMimeType, err = plugin.ReadCover(item.BundlePath, manifest)
		if err != nil {
			return nil, err
		}
	}
	result.ConfigSchemaJson, _ = json.Marshal(manifest.UserConfig)
	config, err := s.store.GetPluginConfig(ctx, userID, item.ID)
	if err != nil {
		return nil, err
	}
	var values map[string]any
	_ = json.Unmarshal([]byte(config.ValuesJSON), &values)
	if values == nil {
		values = map[string]any{}
	}
	if _, err := manifest.ResolveConfig(values, ""); err != nil {
		result.Configured = false
	}
	for key, field := range manifest.UserConfig {
		if field.Sensitive {
			if value, ok := values[key].(string); ok && value != "" {
				result.ConfiguredSecrets = append(result.ConfiguredSecrets, key)
			}
			delete(values, key)
		}
	}
	result.ConfigValuesJson, _ = json.Marshal(values)
	return result, nil
}

func targetMatches(installed, required string) bool {
	return installed == "portable" || installed == required || installed == strings.SplitN(required, "-", 2)[0]+"-any"
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
