// Package plugin устанавливает и разрешает MCPB bundles. Сам MCP/MCP Apps контракт
// остаётся стандартным; Brigade добавляет только metadata постоянного experience.
package plugin

import (
	"archive/zip"
	"context"
	"debug/elf"
	"debug/macho"
	"debug/pe"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
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
var validConfigKey = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
var configRef = regexp.MustCompile(`\$\{user_config\.([A-Za-z0-9_-]+)\}`)
var secretRef = regexp.MustCompile(`\$\{secret\.([A-Za-z0-9_]+)\}`)

type ConfigField struct {
	Type        string          `json:"type"`
	Title       string          `json:"title"`
	Description string          `json:"description"`
	Required    bool            `json:"required"`
	Multiple    bool            `json:"multiple"`
	Sensitive   bool            `json:"sensitive"`
	Default     json.RawMessage `json:"default"`
	Min         *float64        `json:"min"`
	Max         *float64        `json:"max"`
}

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
	UserConfig map[string]ConfigField `json:"user_config"`
	Meta       struct {
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

// ResolveConfig применяет defaults MCPB и проверяет значения до запуска исполняемого bundle.
func (m Manifest) ResolveConfig(input map[string]any, home string) (map[string]any, error) {
	values := make(map[string]any, len(m.UserConfig))
	for key, field := range m.UserConfig {
		value, ok := input[key]
		if !ok && len(field.Default) > 0 {
			if err := json.Unmarshal(field.Default, &value); err != nil {
				return nil, fmt.Errorf("plugin: invalid default for %q: %w", key, err)
			}
			ok = true
		}
		if !ok || emptyConfigValue(value) {
			if field.Required {
				return nil, fmt.Errorf("plugin: required config %q is missing", key)
			}
			continue
		}
		if err := validateConfigValue(key, field, value); err != nil {
			return nil, err
		}
		values[key] = expandHome(value, home)
	}
	return values, nil
}

func validateConfigValue(key string, field ConfigField, value any) error {
	if field.Multiple {
		items, ok := value.([]any)
		if !ok {
			return fmt.Errorf("plugin: config %q must be an array", key)
		}
		for _, item := range items {
			copy := field
			copy.Multiple = false
			if err := validateConfigValue(key, copy, item); err != nil {
				return err
			}
		}
		return nil
	}
	switch field.Type {
	case "string", "file", "directory":
		if _, ok := value.(string); !ok {
			return fmt.Errorf("plugin: config %q must be a string", key)
		}
	case "number":
		number, ok := value.(float64)
		if !ok {
			return fmt.Errorf("plugin: config %q must be a number", key)
		}
		if field.Min != nil && number < *field.Min {
			return fmt.Errorf("plugin: config %q is below its minimum", key)
		}
		if field.Max != nil && number > *field.Max {
			return fmt.Errorf("plugin: config %q exceeds its maximum", key)
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("plugin: config %q must be a boolean", key)
		}
	default:
		return fmt.Errorf("plugin: config %q has unsupported type %q", key, field.Type)
	}
	return nil
}

func emptyConfigValue(value any) bool {
	switch value := value.(type) {
	case nil:
		return true
	case string:
		return strings.TrimSpace(value) == ""
	case []any:
		return len(value) == 0
	default:
		return false
	}
}

func expandHome(value any, home string) any {
	if home == "" {
		return value
	}
	switch value := value.(type) {
	case string:
		value = strings.ReplaceAll(value, "${HOME}", home)
		value = strings.ReplaceAll(value, "${DESKTOP}", filepath.Join(home, "Desktop"))
		return strings.ReplaceAll(value, "${DOCUMENTS}", filepath.Join(home, "Documents"))
	case []any:
		for index := range value {
			value[index] = expandHome(value[index], home)
		}
	}
	return value
}

// MCPServer возвращает стандартный stdio-конфиг ACP для уже распакованного bundle.
func (m Manifest) MCPServer(root string, values map[string]any, secrets map[string]string) (acpsdk.McpServer, error) {
	entryPoint := filepath.Join(root, filepath.FromSlash(m.Server.EntryPoint))
	command := entryPoint
	argsTemplate := append([]string(nil), m.Server.MCPConfig.Args...)
	for index := range argsTemplate {
		argsTemplate[index] = strings.ReplaceAll(argsTemplate[index], "${__dirname}", root)
	}
	args, err := expandArgs(argsTemplate, values, secrets)
	if err != nil {
		return acpsdk.McpServer{}, err
	}
	if m.Server.MCPConfig.Command != "" {
		command, err = expandString(strings.ReplaceAll(m.Server.MCPConfig.Command, "${__dirname}", root), values, secrets)
		if err != nil {
			return acpsdk.McpServer{}, err
		}
	} else {
		switch m.Server.Type {
		case "node":
			command, args = "node", append([]string{entryPoint}, args...)
		case "python":
			command, args = "python3", append([]string{entryPoint}, args...)
		case "uv":
			command, args = "uv", append([]string{"--directory", root, "run", entryPoint}, args...)
		}
	}
	env := make([]acpsdk.EnvVariable, 0, len(m.Server.MCPConfig.Env))
	for name, value := range m.Server.MCPConfig.Env {
		value, err = expandString(strings.ReplaceAll(value, "${__dirname}", root), values, secrets)
		if err != nil {
			return acpsdk.McpServer{}, err
		}
		env = append(env, acpsdk.EnvVariable{Name: name, Value: value})
	}
	return acpsdk.McpServer{Stdio: &acpsdk.McpServerStdio{
		Name: m.Title(), Command: command, Args: args, Env: env,
	}}, nil
}

func expandArgs(input []string, values map[string]any, secrets map[string]string) ([]string, error) {
	var out []string
	for _, arg := range input {
		match := configRef.FindStringSubmatch(arg)
		if len(match) == 2 && match[0] == arg {
			if list, ok := values[match[1]].([]any); ok {
				for _, value := range list {
					out = append(out, fmt.Sprint(value))
				}
				continue
			}
		}
		value, err := expandString(arg, values, secrets)
		if err != nil {
			return nil, err
		}
		out = append(out, value)
	}
	return out, nil
}

func expandString(input string, values map[string]any, secrets map[string]string) (string, error) {
	var missing string
	output := configRef.ReplaceAllStringFunc(input, func(ref string) string {
		key := configRef.FindStringSubmatch(ref)[1]
		value, ok := values[key]
		if !ok {
			missing = key
			return ref
		}
		return fmt.Sprint(value)
	})
	output = secretRef.ReplaceAllStringFunc(output, func(ref string) string {
		key := secretRef.FindStringSubmatch(ref)[1]
		value, ok := secrets[key]
		if !ok {
			missing = key
			return ref
		}
		return value
	})
	if missing != "" {
		return "", fmt.Errorf("plugin: required config %q is missing", missing)
	}
	return output, nil
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
	case "uv":
		if m.ManifestVersion != "0.4" {
			return errors.New("plugin: uv server requires manifest_version 0.4")
		}
	default:
		return fmt.Errorf("plugin: unsupported MCPB server.type %q", m.Server.Type)
	}
	if m.Meta.Brigade.Experience.EntryTool == "" {
		return errors.New("plugin: _meta.brigade.experience.entry_tool is required")
	}
	if cover := m.Meta.Brigade.Experience.Cover; cover != "" && !safeRelative(cover) {
		return errors.New("plugin: _meta.brigade.experience.cover must be a safe relative path")
	}
	for key, field := range m.UserConfig {
		if !validConfigKey.MatchString(key) {
			return fmt.Errorf("plugin: invalid user_config key %q", key)
		}
		switch field.Type {
		case "string", "number", "boolean", "file", "directory":
		default:
			return fmt.Errorf("plugin: config %q has unsupported type %q", key, field.Type)
		}
		if field.Sensitive && field.Type != "string" {
			return fmt.Errorf("plugin: sensitive config %q must be a string", key)
		}
		if field.Multiple && field.Type != "file" && field.Type != "directory" {
			return fmt.Errorf("plugin: multiple config %q must be a file or directory", key)
		}
		if len(field.Default) > 0 {
			var value any
			if err := json.Unmarshal(field.Default, &value); err != nil {
				return fmt.Errorf("plugin: invalid default for %q: %w", key, err)
			}
			if field.Required && emptyConfigValue(value) {
				return fmt.Errorf("plugin: required config %q has an empty default", key)
			}
			if !emptyConfigValue(value) {
				if err := validateConfigValue(key, field, value); err != nil {
					return err
				}
			}
		}
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
	return m.InstallFor(ctx, "", source)
}

func (m *Manager) InstallFor(ctx context.Context, userID, source string) (store.Plugin, error) {
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
	if userID != "" {
		if existing, err := m.store.GetPlugin(ctx, userID, manifest.Name, "", ""); err == nil && existing.OwnerID == "" {
			return store.Plugin{}, fmt.Errorf("plugin: id %q is reserved by a system app", manifest.Name)
		} else if err != nil && !errors.Is(err, store.ErrNotFound) {
			return store.Plugin{}, err
		}
	}
	ownerDir := "system"
	if userID != "" {
		if filepath.Base(userID) != userID {
			return store.Plugin{}, errors.New("plugin: invalid user id")
		}
		ownerDir = filepath.Join("users", userID)
	}
	parent := filepath.Join(m.dir, ownerDir, manifest.Name, manifest.Version)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return store.Plugin{}, fmt.Errorf("plugin: create plugin directory: %w", err)
	}
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
	target, err := bundleTarget(manifest, entry)
	if err != nil {
		return store.Plugin{}, err
	}
	if len(manifest.Compatibility.Platforms) > 0 && target != "portable" {
		platform := strings.SplitN(target, "-", 2)[0]
		compatible := false
		for _, allowed := range manifest.Compatibility.Platforms {
			compatible = compatible || allowed == platform
		}
		if !compatible {
			return store.Plugin{}, fmt.Errorf("plugin: binary is %s but manifest does not allow %s", target, platform)
		}
	}
	if installed, err := m.store.GetPlugin(ctx, userID, manifest.Name, manifest.Version, target); err == nil && installed.OwnerID == userID {
		if _, statErr := os.Stat(filepath.Join(installed.BundlePath, filepath.FromSlash(manifest.Server.EntryPoint))); statErr == nil {
			if err := m.store.PutPlugin(ctx, installed); err != nil {
				return store.Plugin{}, err
			}
			return installed, nil
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return store.Plugin{}, fmt.Errorf("plugin: inspect installed entry point: %w", statErr)
		}
	} else if err != nil && !errors.Is(err, store.ErrNotFound) {
		return store.Plugin{}, err
	}
	destination := filepath.Join(parent, target)
	destinationEntry := filepath.Join(destination, filepath.FromSlash(manifest.Server.EntryPoint))
	if _, err := os.Stat(destinationEntry); errors.Is(err, os.ErrNotExist) {
		if err := os.RemoveAll(destination); err != nil {
			return store.Plugin{}, fmt.Errorf("plugin: replace incomplete bundle: %w", err)
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

	installed := store.Plugin{OwnerID: userID, ID: manifest.Name, Name: manifest.Title(), Version: manifest.Version, Target: target,
		BundlePath: destination, Source: source, ManifestJSON: string(raw), InstalledAt: time.Now().UTC()}
	if err := m.store.PutPlugin(ctx, installed); err != nil {
		return store.Plugin{}, err
	}
	return installed, nil
}

func (m *Manager) InstallReaderFor(ctx context.Context, userID, filename string, source io.Reader) (store.Plugin, error) {
	if err := os.MkdirAll(m.dir, 0o700); err != nil {
		return store.Plugin{}, fmt.Errorf("plugin: create cache: %w", err)
	}
	tmp, err := os.CreateTemp(m.dir, ".upload-*.mcpb")
	if err != nil {
		return store.Plugin{}, err
	}
	path := tmp.Name()
	defer os.Remove(path)
	if _, err := io.Copy(tmp, io.LimitReader(source, maxBundleSize+1)); err != nil {
		tmp.Close()
		return store.Plugin{}, fmt.Errorf("plugin: upload: %w", err)
	}
	info, err := tmp.Stat()
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return store.Plugin{}, err
	}
	if info.Size() > maxBundleSize {
		return store.Plugin{}, fmt.Errorf("plugin: bundle exceeds %d bytes", maxBundleSize)
	}
	installed, err := m.InstallFor(ctx, userID, path)
	if err != nil {
		return store.Plugin{}, err
	}
	installed.Source = "upload:" + filepath.Base(filename)
	if err := m.store.PutPlugin(ctx, installed); err != nil {
		return store.Plugin{}, err
	}
	return installed, nil
}

func (m *Manager) Remove(ctx context.Context, id string) error { return m.RemoveFor(ctx, "", id) }

func (m *Manager) RemoveFor(ctx context.Context, userID, id string) error {
	count, err := m.store.PluginSessionCount(ctx, userID, id)
	if err != nil {
		return err
	}
	if count != 0 {
		return fmt.Errorf("plugin: %s is used by %d sessions", id, count)
	}
	if err := m.store.DeletePlugin(ctx, userID, id); err != nil {
		return err
	}
	roots := []string{filepath.Join(m.dir, "system", id)}
	if userID != "" {
		roots = []string{filepath.Join(m.dir, "users", userID, id)}
	} else {
		roots = append(roots, filepath.Join(m.dir, id)) // layout до user-scoped MCP Apps
	}
	for _, root := range roots {
		if err := os.RemoveAll(root); err != nil {
			return fmt.Errorf("plugin: remove files: %w", err)
		}
	}
	return nil
}

func (m *Manager) Update(ctx context.Context, id string) (store.Plugin, error) {
	current, err := m.store.GetPlugin(ctx, "", id, "", "")
	if err != nil {
		return store.Plugin{}, err
	}
	return m.Install(ctx, current.Source)
}

func (m *Manager) UpdateFor(ctx context.Context, userID, id string) (store.Plugin, error) {
	installed, err := m.store.ListPlugins(ctx, userID)
	if err != nil {
		return store.Plugin{}, err
	}
	var updated store.Plugin
	for _, current := range installed {
		if current.OwnerID != userID || current.ID != id {
			continue
		}
		if strings.HasPrefix(current.Source, "upload:") {
			return store.Plugin{}, errors.New("plugin: uploaded bundle must be replaced by another upload")
		}
		updated, err = m.InstallFor(ctx, userID, current.Source)
		if err != nil {
			return store.Plugin{}, err
		}
	}
	if updated.ID == "" {
		return store.Plugin{}, store.ErrNotFound
	}
	return updated, nil
}

func bundleTarget(manifest Manifest, entry string) (string, error) {
	if manifest.Server.Type != "binary" {
		return "portable", nil
	}
	if file, err := elf.Open(entry); err == nil {
		defer file.Close()
		arch := map[elf.Machine]string{elf.EM_X86_64: "amd64", elf.EM_AARCH64: "arm64"}[file.Machine]
		if arch != "" {
			return "linux-" + arch, nil
		}
	}
	if file, err := macho.Open(entry); err == nil {
		defer file.Close()
		arch := map[macho.Cpu]string{macho.CpuAmd64: "amd64", macho.CpuArm64: "arm64"}[file.Cpu]
		if arch != "" {
			return "darwin-" + arch, nil
		}
	}
	if file, err := pe.Open(entry); err == nil {
		defer file.Close()
		arch := map[uint16]string{pe.IMAGE_FILE_MACHINE_AMD64: "amd64", pe.IMAGE_FILE_MACHINE_ARM64: "arm64"}[file.Machine]
		if arch != "" {
			return "windows-" + arch, nil
		}
	}
	if len(manifest.Compatibility.Platforms) == 1 {
		return manifest.Compatibility.Platforms[0] + "-any", nil
	}
	return "", errors.New("plugin: binary platform cannot be determined")
}

func RuntimeTarget(docker bool) string {
	if docker {
		return "linux-amd64"
	}
	return runtime.GOOS + "-" + runtime.GOARCH
}

func SupportsTarget(manifest Manifest, target string) bool {
	if len(manifest.Compatibility.Platforms) == 0 {
		return true
	}
	platform := strings.SplitN(target, "-", 2)[0]
	if platform == "windows" {
		platform = "win32"
	}
	for _, allowed := range manifest.Compatibility.Platforms {
		if allowed == platform {
			return true
		}
	}
	return false
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
		client := &http.Client{CheckRedirect: func(next *http.Request, _ []*http.Request) error {
			if u.Scheme == "https" && next.URL.Scheme != "https" {
				return errors.New("plugin: HTTPS download redirected to an insecure URL")
			}
			return nil
		}}
		resp, err := client.Do(req)
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
