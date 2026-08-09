package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMoveUserDirectories(t *testing.T) {
	root := t.TempDir()
	oldDir := filepath.Join(root, "old")
	if err := os.Mkdir(oldDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(oldDir, "data"), []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}
	moved, err := moveUserDirectories([]string{root}, "old", "new")
	if err != nil {
		t.Fatal(err)
	}
	if len(moved) != 1 {
		t.Fatalf("moved=%d", len(moved))
	}
	if data, err := os.ReadFile(filepath.Join(root, "new", "data")); err != nil || string(data) != "ok" {
		t.Fatalf("data=%q err=%v", data, err)
	}
}

func TestDefaultConfigPathUsesEnvironment(t *testing.T) {
	t.Setenv("BRIGADE_CONFIG", "/etc/brigade/config.yaml")
	if got := defaultConfigPath(); got != "/etc/brigade/config.yaml" {
		t.Fatalf("defaultConfigPath=%q", got)
	}
	t.Setenv("BRIGADE_CONFIG", "")
	if got := defaultConfigPath(); got != "config.yaml" {
		t.Fatalf("defaultConfigPath without env=%q", got)
	}
}
