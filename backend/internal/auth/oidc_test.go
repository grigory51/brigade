package auth

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/grigory51/brigade/backend/internal/store"
)

func TestLoginExternalKeepsIdentityStableAndDoesNotLinkByUsername(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "auth.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	service := NewService(st.DB(), "test-secret", time.Minute, time.Hour)
	if err := service.EnsureSeedUser(context.Background(), "grisha", "password"); err != nil {
		t.Fatal(err)
	}

	first, err := service.LoginExternal(context.Background(), "https://id.example", "subject-1", "grisha")
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.LoginExternal(context.Background(), "https://id.example", "subject-1", "renamed")
	if err != nil {
		t.Fatal(err)
	}
	if first.User.ID != second.User.ID || first.User.Username != second.User.Username {
		t.Fatalf("identity changed: %#v -> %#v", first.User, second.User)
	}
	if first.User.Username == "grisha" {
		t.Fatal("external identity linked to password username")
	}
}

func TestContainsRoleSupportsZitadelRoleClaim(t *testing.T) {
	claim := map[string]any{"brigade:user": map[string]any{"project-id": "example.com"}}
	if !containsRole(claim, "brigade:user") {
		t.Fatal("required role was not found")
	}
	if containsRole(claim, "brigade:admin") {
		t.Fatal("unexpected role was accepted")
	}
}
