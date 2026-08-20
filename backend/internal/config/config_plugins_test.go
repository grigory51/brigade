package config

import (
	"path/filepath"
	"testing"
	"time"
)

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
