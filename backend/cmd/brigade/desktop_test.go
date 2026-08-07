package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/grigory51/brigade/backend/internal/config"
)

// TestEnsureDesktopConfig: сгенерированный десктоп-конфиг валиден (грузится config.Load),
// mode=local, секрет непустой; повторный вызов НЕ меняет секрет (стабильность — иначе
// сломалась бы расшифровка секретов в БД).
func TestEnsureDesktopConfig(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")

	if err := ensureDesktopConfig(dir, cfgPath); err != nil {
		t.Fatalf("ensureDesktopConfig: %v", err)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("сгенерированный конфиг не грузится: %v", err)
	}
	if cfg.Mode != config.ModeLocal {
		t.Errorf("mode = %q, want local", cfg.Mode)
	}
	if cfg.JWT.Secret == "" {
		t.Error("jwt.secret пустой")
	}
	if !filepath.IsAbs(cfg.SQLitePath) {
		t.Errorf("sqlite_path не абсолютный: %q", cfg.SQLitePath)
	}

	secret := cfg.JWT.Secret
	if err := ensureDesktopConfig(dir, cfgPath); err != nil {
		t.Fatalf("ensureDesktopConfig (2): %v", err)
	}
	cfg2, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("перечитать конфиг: %v", err)
	}
	if cfg2.JWT.Secret != secret {
		t.Error("секрет изменился при повторном вызове — нестабилен")
	}
}

func TestEnsureDesktopAgentRuntimeRequiresToolsOnFirstInstallFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses a shell script")
	}
	appDir, resources := brokenDesktopRuntime(t)
	if err := ensureDesktopAgentRuntime(appDir, resources); err == nil {
		t.Fatal("expected first install failure")
	}
}

func TestEnsureDesktopAgentRuntimeKeepsCompleteRuntimeOnUpdateFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses a shell script")
	}
	appDir, resources := brokenDesktopRuntime(t)
	binDir := filepath.Join(appDir, "agent-runtime", "node_modules", ".bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"claude", "claude-agent-acp", "codex", "codex-acp"} {
		if err := os.WriteFile(filepath.Join(binDir, name), []byte(""), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := ensureDesktopAgentRuntime(appDir, resources); err != nil {
		t.Fatalf("existing runtime must survive update failure: %v", err)
	}
}

func brokenDesktopRuntime(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	resources := filepath.Join(root, "Resources")
	nodeBin := filepath.Join(resources, "node", "bin")
	if err := os.MkdirAll(filepath.Join(resources, "node", "lib", "node_modules", "npm", "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(nodeBin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(resources, "agent-package.json"), []byte(`{"dependencies":{"example":"latest"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nodeBin, "node"), []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(root, "data"), resources
}
