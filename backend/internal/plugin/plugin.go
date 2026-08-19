// Package plugin устанавливает и разрешает MCPB bundles. Сам MCP/MCP Apps контракт
// остаётся стандартным; Brigade добавляет только metadata постоянного experience.
package plugin

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	acpsdk "github.com/coder/acp-go-sdk"

	"github.com/grigory51/brigade/backend/internal/store"
)

const (
	maxBundleSize = 1 << 30
	maxCoverSize  = 1 << 20
)

var validID = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)

// Manifest — используемая Brigade часть MCPB manifest 0.3/0.4.
type Manifest struct {
	ManifestVersion string `json:"manifest_version"`
	Name            string `json:"name"`
	DisplayName     string `json:"display_name"`
	Description     string `json:"description"`
	Version         string `json:"version"`
	Icon            string `json:"icon"`
	Server          struct {
		Type       string `json:"type"`
		EntryPoint string `json:"entry_point"`
		MCPConfig  struct {
			Command string            `json:"command"`
			Args    []string          `json:"args"`
			Env     map[string]string `json:"env"`
		} `json:"mcp_config"`
	} `json:"server"`
	Compatibility struct {
		Platforms []string `json:"platforms"`
	} `json:"compatibility"`
	Meta struct {
		Brigade struct {
			Experience struct {
				EntryTool string `json:"entry_tool"`
				Cover     string `json:"cover"`
			} `json:"experience"`
		} `json:"brigade"`
	} `json:"_meta"`
}

func (m Manifest) Title() string {
	if m.DisplayName != "" {
		return m.DisplayName
	}
	return m.Name
}

// MCPServer возвращает стандартный stdio-конфиг ACP для уже распакованного bundle.
func (m Manifest) MCPServer(root string) acpsdk.McpServer {
	entryPoint := filepath.Join(root, filepath.FromSlash(m.Server.EntryPoint))
	command := entryPoint
	args := append([]string(nil), m.Server.MCPConfig.Args...)
	switch m.Server.Type {
	case "node":
		command, args = "node", append([]string{entryPoint}, args...)
	case "python":
		command, args = "python3", append([]string{entryPoint}, args...)
	}
	if m.Server.MCPConfig.Command != "" {
		command = strings.ReplaceAll(m.Server.MCPConfig.Command, "${__dirname}", root)
	}
	env := make([]acpsdk.EnvVariable, 0, len(m.Server.MCPConfig.Env))
	for name, value := range m.Server.MCPConfig.Env {
		env = append(env, acpsdk.EnvVariable{Name: name, Value: strings.ReplaceAll(value, "${__dirname}", root)})
	}
	return acpsdk.McpServer{Stdio: &acpsdk.McpServerStdio{
		Name: m.Title(), Command: command, Args: args, Env: env,
	}}
}

func (m Manifest) Validate() error {
	if m.ManifestVersion != "0.3" && m.ManifestVersion != "0.4" {
		return fmt.Errorf("plugin: unsupported MCPB manifest_version %q", m.ManifestVersion)
	}
	if !validID.MatchString(m.Name) || !validID.MatchString(m.Version) {
		return errors.New("plugin: manifest requires a valid name and version")
	}
	if m.Server.EntryPoint == "" || !safeRelative(m.Server.EntryPoint) {
		return errors.New("plugin: server.entry_point must be a safe relative path")
	}
	switch m.Server.Type {
	case "binary", "node", "python":
	default:
		return fmt.Errorf("plugin: unsupported MCPB server.type %q", m.Server.Type)
	}
	if m.Meta.Brigade.Experience.EntryTool == "" {
		return errors.New("plugin: _meta.brigade.experience.entry_tool is required")
	}
	if cover := m.Meta.Brigade.Experience.Cover; cover != "" && !safeRelative(cover) {
		return errors.New("plugin: _meta.brigade.experience.cover must be a safe relative path")
	}
	return nil
}

func ReadCover(root string, manifest Manifest) ([]byte, string, error) {
	cover := manifest.Meta.Brigade.Experience.Cover
	if cover == "" {
		return nil, "", nil
	}
	mimeType := map[string]string{
		".svg": "image/svg+xml", ".png": "image/png", ".jpg": "image/jpeg",
		".jpeg": "image/jpeg", ".webp": "image/webp",
	}[strings.ToLower(filepath.Ext(cover))]
	if mimeType == "" {
		return nil, "", errors.New("plugin: cover has unsupported format")
	}
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(cover)))
	if err != nil {
		return nil, "", fmt.Errorf("plugin: read cover: %w", err)
	}
	if len(data) > maxCoverSize {
		return nil, "", errors.New("plugin: cover exceeds 1 MiB")
	}
	return data, mimeType, nil
}

func safeRelative(path string) bool {
	clean := filepath.Clean(filepath.FromSlash(path))
	return path != "" && clean != "." && !filepath.IsAbs(clean) && clean != ".." && !strings.HasPrefix(clean, ".."+string(filepath.Separator))
}

func ParseManifest(raw []byte) (Manifest, error) {
	var manifest Manifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return Manifest{}, fmt.Errorf("plugin: parse manifest: %w", err)
	}
	if err := manifest.Validate(); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

type Manager struct {
	dir   string
	store *store.Store
}

func New(dir string, st *store.Store) *Manager { return &Manager{dir: dir, store: st} }

func (m *Manager) Install(ctx context.Context, source string) (store.Plugin, error) {
	if err := os.MkdirAll(m.dir, 0o700); err != nil {
		return store.Plugin{}, fmt.Errorf("plugin: create cache: %w", err)
	}
	tmp, err := os.CreateTemp(m.dir, ".download-*.mcpb")
	if err != nil {
		return store.Plugin{}, fmt.Errorf("plugin: temp bundle: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := copySource(ctx, tmp, source); err != nil {
		tmp.Close()
		return store.Plugin{}, err
	}
	if err := tmp.Close(); err != nil {
		return store.Plugin{}, fmt.Errorf("plugin: close bundle: %w", err)
	}

	zr, err := zip.OpenReader(tmpPath)
	if err != nil {
		return store.Plugin{}, fmt.Errorf("plugin: open MCPB: %w", err)
	}
	defer zr.Close()
	raw, err := manifestFromZip(&zr.Reader)
	if err != nil {
		return store.Plugin{}, err
	}
	manifest, err := ParseManifest(raw)
	if err != nil {
		return store.Plugin{}, err
	}
	if installed, err := m.store.GetPlugin(ctx, manifest.Name, manifest.Version); err == nil {
		if err := m.store.PutPlugin(ctx, installed); err != nil {
			return store.Plugin{}, err
		}
		return installed, nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return store.Plugin{}, err
	}

	parent := filepath.Join(m.dir, manifest.Name)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return store.Plugin{}, fmt.Errorf("plugin: create plugin directory: %w", err)
	}
	destination := filepath.Join(parent, manifest.Version)
	if _, err := os.Stat(destination); errors.Is(err, os.ErrNotExist) {
		staging, err := os.MkdirTemp(parent, ".install-*")
		if err != nil {
			return store.Plugin{}, fmt.Errorf("plugin: staging: %w", err)
		}
		defer os.RemoveAll(staging)
		if err := extract(&zr.Reader, staging); err != nil {
			return store.Plugin{}, err
		}
		entry := filepath.Join(staging, filepath.FromSlash(manifest.Server.EntryPoint))
		if _, err := os.Stat(entry); err != nil {
			return store.Plugin{}, fmt.Errorf("plugin: entry point %q: %w", manifest.Server.EntryPoint, err)
		}
		if manifest.Server.Type == "binary" {
			if err := os.Chmod(entry, 0o755); err != nil {
				return store.Plugin{}, fmt.Errorf("plugin: mark entry point executable: %w", err)
			}
		}
		if _, _, err := ReadCover(staging, manifest); err != nil {
			return store.Plugin{}, err
		}
		if err := os.Rename(staging, destination); err != nil {
			return store.Plugin{}, fmt.Errorf("plugin: publish bundle: %w", err)
		}
	} else if err != nil {
		return store.Plugin{}, fmt.Errorf("plugin: inspect destination: %w", err)
	}

	installed := store.Plugin{ID: manifest.Name, Name: manifest.Title(), Version: manifest.Version,
		BundlePath: destination, Source: source, ManifestJSON: string(raw), InstalledAt: time.Now().UTC()}
	if err := m.store.PutPlugin(ctx, installed); err != nil {
		return store.Plugin{}, err
	}
	return installed, nil
}

func (m *Manager) Remove(ctx context.Context, id string) error {
	count, err := m.store.PluginSessionCount(ctx, id)
	if err != nil {
		return err
	}
	if count != 0 {
		return fmt.Errorf("plugin: %s is used by %d sessions", id, count)
	}
	if err := m.store.DeletePlugin(ctx, id); err != nil {
		return err
	}
	if err := os.RemoveAll(filepath.Join(m.dir, id)); err != nil {
		return fmt.Errorf("plugin: remove files: %w", err)
	}
	return nil
}

func (m *Manager) Update(ctx context.Context, id string) (store.Plugin, error) {
	current, err := m.store.GetPlugin(ctx, id, "")
	if err != nil {
		return store.Plugin{}, err
	}
	return m.Install(ctx, current.Source)
}

func (m *Manager) ValidateBundle(path string) (Manifest, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return Manifest{}, fmt.Errorf("plugin: open MCPB: %w", err)
	}
	defer zr.Close()
	raw, err := manifestFromZip(&zr.Reader)
	if err != nil {
		return Manifest{}, err
	}
	manifest, err := ParseManifest(raw)
	if err != nil {
		return Manifest{}, err
	}
	found := false
	for _, file := range zr.File {
		if filepath.ToSlash(file.Name) == filepath.ToSlash(manifest.Server.EntryPoint) {
			found = true
			break
		}
	}
	if !found {
		return Manifest{}, fmt.Errorf("plugin: entry point %q is missing", manifest.Server.EntryPoint)
	}
	return manifest, nil
}

func copySource(ctx context.Context, dst *os.File, source string) error {
	u, err := url.Parse(source)
	if err == nil && (u.Scheme == "http" || u.Scheme == "https") {
		if u.Scheme != "https" && u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost" {
			return errors.New("plugin: remote bundle URL must use HTTPS")
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
		if err != nil {
			return fmt.Errorf("plugin: create download request: %w", err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return fmt.Errorf("plugin: download: %w", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("plugin: download: %s", resp.Status)
		}
		_, err = io.Copy(dst, io.LimitReader(resp.Body, maxBundleSize+1))
		if err != nil {
			return fmt.Errorf("plugin: download: %w", err)
		}
	} else {
		src, err := os.Open(source)
		if err != nil {
			return fmt.Errorf("plugin: open bundle: %w", err)
		}
		defer src.Close()
		if _, err := io.Copy(dst, io.LimitReader(src, maxBundleSize+1)); err != nil {
			return fmt.Errorf("plugin: copy bundle: %w", err)
		}
	}
	if info, err := dst.Stat(); err != nil {
		return fmt.Errorf("plugin: stat bundle: %w", err)
	} else if info.Size() > maxBundleSize {
		return fmt.Errorf("plugin: bundle exceeds %d bytes", maxBundleSize)
	}
	return nil
}

func manifestFromZip(zr *zip.Reader) ([]byte, error) {
	for _, file := range zr.File {
		if file.Name != "manifest.json" {
			continue
		}
		r, err := file.Open()
		if err != nil {
			return nil, fmt.Errorf("plugin: open manifest: %w", err)
		}
		defer r.Close()
		raw, err := io.ReadAll(io.LimitReader(r, 1<<20))
		if err != nil {
			return nil, fmt.Errorf("plugin: read manifest: %w", err)
		}
		return raw, nil
	}
	return nil, errors.New("plugin: MCPB has no manifest.json")
}

func extract(zr *zip.Reader, destination string) error {
	var total uint64
	for _, file := range zr.File {
		if !safeRelative(file.Name) {
			return fmt.Errorf("plugin: unsafe bundle path %q", file.Name)
		}
		if file.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("plugin: symlinks are not allowed: %q", file.Name)
		}
		if file.UncompressedSize64 > maxBundleSize-total {
			return fmt.Errorf("plugin: extracted bundle exceeds %d bytes", maxBundleSize)
		}
		total += file.UncompressedSize64
		path := filepath.Join(destination, filepath.FromSlash(file.Name))
		if file.FileInfo().IsDir() {
			if err := os.MkdirAll(path, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		r, err := file.Open()
		if err != nil {
			return err
		}
		out, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, file.Mode().Perm()|0o400)
		if err == nil {
			_, err = io.Copy(out, r)
			if closeErr := out.Close(); err == nil {
				err = closeErr
			}
		}
		r.Close()
		if err != nil {
			return fmt.Errorf("plugin: extract %q: %w", file.Name, err)
		}
	}
	return nil
}
