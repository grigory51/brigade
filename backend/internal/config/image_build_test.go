package config

import (
	"strings"
	"testing"
	"time"
)

func TestImageBuildConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name  string
		build ImageBuildConfig
		valid bool
	}{
		{"defaults", ImageBuildConfig{}, true},
		{"explicit", ImageBuildConfig{Backend: "docker", Timeout: time.Hour, MaxConcurrent: 4}, true},
		{"unsupported", ImageBuildConfig{Backend: "gitlab"}, false},
		{"negative timeout", ImageBuildConfig{Timeout: -time.Second}, false},
		{"negative concurrency", ImageBuildConfig{MaxConcurrent: -1}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Config{ImageBuild: tc.build}
			err := cfg.Validate()
			if !tc.valid {
				if err == nil || !strings.Contains(err.Error(), "image_build") {
					t.Fatalf("validation: %v", err)
				}
				return
			}
			if cfg.ImageBuild.Backend != "docker" || cfg.ImageBuild.Timeout <= 0 || cfg.ImageBuild.MaxConcurrent <= 0 {
				t.Fatalf("defaults: %+v", cfg.ImageBuild)
			}
		})
	}
}
