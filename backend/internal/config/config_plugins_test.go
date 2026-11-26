package config

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPluginsBundlesPaths(t *testing.T) {
	for _, tc := range []struct {
		name    string
		bundles []string
		wantErr bool
	}{
		{name: "omitted"},
		{name: "local", bundles: []string{"bundles/app.mcpb"}},
		{name: "empty", bundles: []string{" "}, wantErr: true},
		{name: "https", bundles: []string{"https://example.com/app.mcpb"}, wantErr: true},
		{name: "localhost", bundles: []string{"http://localhost/app.mcpb"}, wantErr: true},
		{name: "file URL", bundles: []string{"file:///tmp/app.mcpb"}, wantErr: true},
		{name: "duplicate", bundles: []string{"bundles/app.mcpb", "./bundles/app.mcpb"}, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			cfg := Config{
				Addr: ":8080", SQLitePath: filepath.Join(dir, "brigade.db"), WorkDir: dir,
				JWT:  JWTConfig{Secret: "secret", AccessTTL: time.Minute, RefreshTTL: time.Hour},
				Auth: AuthConfig{PasswordEnabled: true}, Seed: SeedConfig{Username: "admin", Password: "admin"},
				Plugins: PluginsConfig{Bundles: tc.bundles},
			}
			err := cfg.Validate()
			if tc.wantErr {
				if err == nil || !strings.Contains(err.Error(), "plugins.bundles") {
					t.Fatalf("Validate() = %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			for _, path := range cfg.Plugins.Bundles {
				if !filepath.IsAbs(path) {
					t.Fatalf("bundle path is not absolute: %q", path)
				}
			}
			if cfg.Coding.Endpoints == nil {
				t.Fatal("coding defaults not applied")
			}
			cfg.Coding.Endpoints = cfg.Coding.Endpoints[:0]
			if err := cfg.Validate(); err != nil || len(cfg.Coding.Endpoints) != 0 {
				t.Fatalf("explicit empty coding endpoints replaced: %+v, %v", cfg.Coding, err)
			}
		})
	}
}

func TestPluginsDirDefaultsBesideDatabase(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{
		Addr:       ":8080",
		SQLitePath: filepath.Join(dir, "brigade.db"),
		WorkDir:    filepath.Join(dir, "workspace"),
		JWT:        JWTConfig{Secret: "secret", AccessTTL: time.Minute, RefreshTTL: time.Hour},
		Auth:       AuthConfig{PasswordEnabled: true},
		Seed:       SeedConfig{Username: "admin", Password: "admin"},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, "plugins"); cfg.PluginsDir != want {
		t.Fatalf("PluginsDir = %q, want %q", cfg.PluginsDir, want)
	}
}
