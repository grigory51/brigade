package plugin

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/grigory51/brigade/backend/internal/store"
)

func TestManifestAndSafeExtraction(t *testing.T) {
	raw := []byte(`{"manifest_version":"0.3","name":"cad","version":"1.0.0","server":{"type":"binary","entry_point":"server/cad"},"_meta":{"brigade":{"experience":{"entry_tool":"cad.open"}}}}`)
	if _, err := ParseManifest(raw); err != nil {
		t.Fatal(err)
	}
	unsafeVersion := []byte(`{"manifest_version":"0.3","name":"cad","version":"..","server":{"type":"binary","entry_point":"server/cad"},"_meta":{"brigade":{"experience":{"entry_tool":"cad.open"}}}}`)
	if _, err := ParseManifest(unsafeVersion); err == nil {
		t.Fatal("unsafe version must be rejected")
	}
	var data bytes.Buffer
	zw := zip.NewWriter(&data)
	w, _ := zw.Create("../escape")
	_, _ = w.Write([]byte("x"))
	_ = zw.Close()
	zr, _ := zip.NewReader(bytes.NewReader(data.Bytes()), int64(data.Len()))
	dir := t.TempDir()
	if err := extract(zr, dir); err == nil {
		t.Fatal("path traversal must be rejected")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "escape")); !os.IsNotExist(err) {
		t.Fatal("archive escaped destination")
	}
}

func TestUpdateKeepsPinnedVersion(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "brigade.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	source := filepath.Join(t.TempDir(), "app.mcpb")
	writeBundle := func(version string) {
		var data bytes.Buffer
		archive := zip.NewWriter(&data)
		manifest, _ := archive.Create("manifest.json")
		_, _ = manifest.Write([]byte(`{"manifest_version":"0.3","name":"app","version":"` + version + `","server":{"type":"python","entry_point":"server.py"},"_meta":{"brigade":{"experience":{"entry_tool":"app.open"}}}}`))
		entry, _ := archive.Create("server.py")
		_, _ = entry.Write([]byte("pass"))
		_ = archive.Close()
		if err := os.WriteFile(source, data.Bytes(), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	manager := New(t.TempDir(), st)
	writeBundle("1.0.0")
	if _, err := manager.InstallFor(ctx, "user", source); err != nil {
		t.Fatal(err)
	}
	writeBundle("2.0.0")
	if updated, err := manager.UpdateFor(ctx, "user", "app"); err != nil || updated.Version != "2.0.0" {
		t.Fatalf("update = %+v, %v", updated, err)
	}
	if pinned, err := st.GetPlugin(ctx, "user", "app", "1.0.0", "portable"); err != nil || pinned.Version != "1.0.0" {
		t.Fatalf("pinned = %+v, %v", pinned, err)
	}
}

func TestReadCover(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "ui"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ui", "cover.svg"), []byte("<svg/>"), 0o644); err != nil {
		t.Fatal(err)
	}
	manifest := Manifest{}
	manifest.Meta.Brigade.Experience.Cover = "ui/cover.svg"
	data, mimeType, err := ReadCover(dir, manifest)
	if err != nil || string(data) != "<svg/>" || mimeType != "image/svg+xml" {
		t.Fatalf("ReadCover() = %q, %q, %v", data, mimeType, err)
	}
}

func TestConfigExpansionAndRuntimeTarget(t *testing.T) {
	manifest := Manifest{}
	manifest.Name = "app"
	manifest.UserConfig = map[string]ConfigField{
		"roots": {Type: "directory", Required: true, Multiple: true},
		"token": {Type: "string", Required: true, Sensitive: true},
		"cache": {Type: "directory", Default: json.RawMessage(`"${HOME}/cache"`)},
	}
	manifest.Server.Type = "binary"
	manifest.Server.EntryPoint = "server/app"
	manifest.Server.MCPConfig.Args = []string{"--roots", "${user_config.roots}"}
	manifest.Server.MCPConfig.Env = map[string]string{"TOKEN": "${user_config.token}"}
	values, err := manifest.ResolveConfig(map[string]any{
		"roots": []any{"/one", "/two"},
		"token": "${secret.APP_TOKEN}",
	}, "/home/agent")
	if err != nil || values["cache"] != "/home/agent/cache" {
		t.Fatalf("config = %#v, %v", values, err)
	}
	server, err := manifest.MCPServer("/plugin", values, map[string]string{"APP_TOKEN": "secret"})
	if err != nil {
		t.Fatal(err)
	}
	if got := server.Stdio.Args; len(got) != 3 || got[1] != "/one" || got[2] != "/two" {
		t.Fatalf("args = %#v", got)
	}
	if got := server.Stdio.Env; len(got) != 1 || got[0].Value != "secret" {
		t.Fatalf("env = %#v", got)
	}
	node := Manifest{}
	node.Server.Type = "node"
	node.Server.EntryPoint = "server.js"
	node.Server.MCPConfig.Command = "node"
	node.Server.MCPConfig.Args = []string{"${__dirname}/server.js"}
	if server, err := node.MCPServer("/plugin", nil, nil); err != nil || len(server.Stdio.Args) != 1 || server.Stdio.Args[0] != "/plugin/server.js" {
		t.Fatalf("explicit command = %+v, %v", server, err)
	}
	uv := Manifest{}
	uv.Server.Type = "uv"
	uv.Server.EntryPoint = "src/server.py"
	if server, err := uv.MCPServer("/plugin", nil, nil); err != nil || server.Stdio.Command != "uv" || len(server.Stdio.Args) != 4 {
		t.Fatalf("uv command = %+v, %v", server, err)
	}
	if _, err := manifest.ResolveConfig(map[string]any{"roots": "not-an-array"}, ""); err == nil {
		t.Fatal("invalid config must be rejected")
	}
	if target := RuntimeTarget(false); target == "" {
		t.Fatal("empty runtime target")
	}
	manifest.Compatibility.Platforms = []string{"darwin"}
	if SupportsTarget(manifest, "linux-amd64") || !SupportsTarget(manifest, "darwin-arm64") {
		t.Fatal("manifest platform compatibility is ignored")
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if target, err := bundleTarget(manifest, executable); err != nil || target != RuntimeTarget(false) {
		t.Fatalf("bundle target = %q, %v", target, err)
	}
}
