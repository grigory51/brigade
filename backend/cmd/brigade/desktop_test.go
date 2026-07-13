package main

import (
	"path/filepath"
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
	// Пути должны быть абсолютными (иначе БД потеряется при запуске из Finder).
	if !filepath.IsAbs(cfg.SQLitePath) {
		t.Errorf("sqlite_path не абсолютный: %q", cfg.SQLitePath)
	}

	// Идемпотентность: повторный вызов не перезаписывает файл (секрет стабилен).
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
