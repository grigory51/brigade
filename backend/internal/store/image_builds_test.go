package store

import (
	"context"
	"testing"
)

func TestImageBuildPersistenceOwnershipAndCAS(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	b := ImageBuild{ID: "one", Name: "tools", Script: "echo hello", Status: "queued"}
	if err := st.StartImageBuild(ctx, "u1", b); err != nil {
		t.Fatal(err)
	}
	if err := st.StartImageBuild(ctx, "missing", b); err == nil {
		t.Fatal("accepted missing user")
	}
	b.Status, b.Log = "building", "installing"
	if err := st.UpdateImageBuild(ctx, "u1", b); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateImageBuild(ctx, "other", b); err == nil {
		t.Fatal("cross-user write accepted")
	}
	got, err := st.GetImageBuild(ctx, "u1")
	if err != nil || got != b {
		t.Fatalf("round trip: %+v %v", got, err)
	}
	if err := st.InterruptImageBuilds(ctx); err != nil {
		t.Fatal(err)
	}
	got, err = st.GetImageBuild(ctx, "u1")
	if err != nil || got.Status != "interrupted" || got.Log != b.Log || got.Script != b.Script {
		t.Fatalf("interrupt: %+v %v", got, err)
	}
	if err := st.StartImageBuild(ctx, "u1", ImageBuild{ID: "two", Script: "new", Status: "queued"}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateImageBuild(ctx, "u1", b); err == nil {
		t.Fatal("stale write accepted")
	}
}

func TestImageBuildUserMigrationAndDeletion(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	if _, err := st.DB().Exec(`INSERT INTO users(id,username,password_hash,created_at) VALUES('u2','oidc','',0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`INSERT INTO auth_identities(provider,subject,user_id,created_at) VALUES('https://id.example','sub','u2',0)`); err != nil {
		t.Fatal(err)
	}
	for _, user := range []string{"u1", "u2"} {
		if err := st.StartImageBuild(ctx, user, ImageBuild{ID: user, Script: user, Status: "failed"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.MigrateUser(ctx, "u1", "u2"); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetImageBuild(ctx, "u2")
	if err != nil || got.Script != "u1" {
		t.Fatalf("migration: %+v %v", got, err)
	}
	if err := st.DeleteUser(ctx, "u2"); err != nil {
		t.Fatal(err)
	}
	got, err = st.GetImageBuild(ctx, "u2")
	if err != nil || got.ID != "" {
		t.Fatalf("deletion: %+v %v", got, err)
	}
}
