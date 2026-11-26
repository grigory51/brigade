package plugin

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/grigory51/brigade/backend/internal/store"
)

var ErrManagedPlugin = errors.New("plugin: managed apps can only be changed through plugins.bundles")

// RegisterBundles проверяет весь локальный набор до публикации файлов и изменения
// каталога. Доступность переключается одной транзакцией; старые версии сохраняются.
// Метод вызывается при старте до restore и обслуживания запросов.
func (m *Manager) RegisterBundles(ctx context.Context, sources []string) error {
	bundles := make([]*preparedBundle, 0, len(sources))
	defer func() {
		for _, bundle := range bundles {
			os.RemoveAll(bundle.staging)
		}
	}()
	seen := make(map[string]bool, len(sources))
	for index, source := range sources {
		if err := ctx.Err(); err != nil {
			return err
		}
		if strings.TrimSpace(source) == "" || strings.Contains(source, "://") || strings.HasPrefix(source, "file:") {
			return fmt.Errorf("plugin: plugins.bundles[%d] must be a local file path", index)
		}
		path, err := filepath.Abs(source)
		if err != nil {
			return fmt.Errorf("plugin: plugins.bundles[%d]: %w", index, err)
		}
		if info, err := os.Stat(path); err != nil {
			return fmt.Errorf("plugin: plugins.bundles[%d] %q: %w", index, path, err)
		} else if !info.Mode().IsRegular() {
			return fmt.Errorf("plugin: plugins.bundles[%d] %q must be a regular file", index, path)
		}
		bundle, err := m.prepareBundle(ctx, path)
		if err != nil {
			return fmt.Errorf("plugin: plugins.bundles[%d] %q: %w", index, path, err)
		}
		bundles = append(bundles, bundle)
		// Один id/target имеет одну текущую версию; порядок YAML не выбирает её.
		key := bundle.plugin.ID + "/" + bundle.plugin.Target
		if seen[key] {
			return fmt.Errorf("plugin: duplicate managed app target %q", key)
		}
		seen[key] = true
	}
	installed := make([]store.Plugin, 0, len(bundles))
	for _, bundle := range bundles {
		item, err := m.publishBundle(ctx, bundle, "", true)
		if err != nil {
			return err
		}
		installed = append(installed, item)
	}
	return m.store.SyncManagedPlugins(ctx, installed, RuntimeTarget(false))
}

// ListFor возвращает доступные варианты; managed скрывает личные установки того
// же id целиком, даже если у managed нет target текущего runtime.
func (m *Manager) ListFor(ctx context.Context, userID string) ([]store.Plugin, error) {
	return m.store.ListPlugins(ctx, userID)
}

// ResolveFor разрешает новый выбор (version == "") или legacy unmanaged pin.
// Для закреплённого владельца следует использовать ResolveForOwner.
func (m *Manager) ResolveFor(ctx context.Context, userID, id, version, target string) (store.Plugin, error) {
	return m.store.GetPlugin(ctx, userID, id, version, target)
}

// ResolveForOwner разрешает точный owner/version, включая отключённые managed.
// Доступ к пользовательской установке другого владельца запрещён.
func (m *Manager) ResolveForOwner(ctx context.Context, userID, ownerID, id, version, target string) (store.Plugin, error) {
	if ownerID != "" && ownerID != userID {
		return store.Plugin{}, store.ErrNotFound
	}
	return m.store.GetPluginForOwner(ctx, ownerID, id, version, target)
}

func (m *Manager) checkUnmanaged(ctx context.Context, userID, id string) error {
	if userID == "" {
		managed, err := m.store.HasManagedPlugin(ctx, id)
		if err != nil {
			return err
		}
		if managed {
			return ErrManagedPlugin
		}
	}
	installed, err := m.store.GetPlugin(ctx, userID, id, "", "")
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if installed.Managed {
		return ErrManagedPlugin
	}
	return nil
}
