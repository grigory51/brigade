package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAuthDefaultsAndPasswordCanBeDisabled(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	base := `addr: ":8080"
sqlite_path: "brigade.db"
work_dir: "workspace"
jwt:
  secret: "test"
  access_ttl: "15m"
  refresh_ttl: "1h"
seed:
  username: "admin"
  password: "admin"
`
	if err := os.WriteFile(path, []byte(base), 0o600); err != nil {
		t.Fatal(err)
	}
	config, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !config.Auth.PasswordEnabled {
		t.Fatal("password login must remain enabled for existing configs")
	}

	oidc := base + `auth:
  password_enabled: false
  oidc:
    issuer: "https://auth.example.com/"
    client_id: "brigade"
    redirect_url: "https://brigade.example.com/auth/oidc/callback"
    required_role: "brigade:user"
`
	if err := os.WriteFile(path, []byte(oidc), 0o600); err != nil {
		t.Fatal(err)
	}
	config, err = Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if config.Auth.PasswordEnabled || config.Auth.OIDC.RoleClaim == "" || len(config.Auth.OIDC.Scopes) == 0 {
		t.Fatalf("unexpected OIDC defaults: %#v", config.Auth)
	}
}
