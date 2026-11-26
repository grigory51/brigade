package plugin

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/grigory51/brigade/backend/internal/store"
)

func managedTestManifest(name, version string) Manifest {
	manifest := Manifest{ManifestVersion: "0.3", Name: name, Version: version}
	manifest.Server.Type = "python"
	manifest.Server.EntryPoint = "server.py"
	manifest.Meta.Brigade.Experience.EntryTool = "app.open"
	return manifest
}

func writeManagedTestBundle(t *testing.T, manifest Manifest, files map[string]string) string {
	t.Helper()
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	var data bytes.Buffer
	archive := zip.NewWriter(&data)
	entries := map[string]string{"manifest.json": string(raw)}
	for name, content := range files {
		entries[name] = content
	}
	for name, content := range entries {
		entry, err := archive.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "app.mcpb")
	if err := os.WriteFile(path, data.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestManagedBundlesLifecycle(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "brigade.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	cache := t.TempDir()
	manager := New(cache, st)
	v1 := writeManagedTestBundle(t, managedTestManifest("app", "1"), map[string]string{"server.py": "first"})
	personal, err := manager.InstallFor(ctx, "user", v1)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.RegisterBundles(ctx, []string{v1}); err != nil {
		t.Fatal(err)
	}
	first, err := manager.ResolveFor(ctx, "user", "app", "", "linux-amd64")
	if err != nil || !first.Managed || first.OwnerID != "" {
		t.Fatalf("managed precedence = %+v, %v", first, err)
	}
	listed, err := manager.ListFor(ctx, "user")
	if err != nil || len(listed) != 1 || !listed[0].Managed {
		t.Fatalf("managed list = %+v, %v", listed, err)
	}
	if err := manager.RegisterBundles(ctx, []string{v1}); err != nil {
		t.Fatal(err)
	}
	again, err := manager.ResolveFor(ctx, "user", "app", "", "portable")
	if err != nil || again.BundlePath != first.BundlePath || !again.InstalledAt.Equal(first.InstalledAt) {
		t.Fatalf("idempotent registration = %+v, %v", again, err)
	}
	for _, owner := range []string{"", "user"} {
		if err := manager.RemoveFor(ctx, owner, "app"); !errors.Is(err, ErrManagedPlugin) {
			t.Fatalf("remove managed for %q = %v", owner, err)
		}
		if _, err := manager.UpdateFor(ctx, owner, "app", v1); !errors.Is(err, ErrManagedPlugin) {
			t.Fatalf("update managed for %q = %v", owner, err)
		}
	}
	v2 := writeManagedTestBundle(t, managedTestManifest("app", "2"), map[string]string{"server.py": "second"})
	if err := manager.RegisterBundles(ctx, []string{v2}); err != nil {
		t.Fatal(err)
	}
	if current, err := manager.ResolveFor(ctx, "user", "app", "", "portable"); err != nil || current.Version != "2" {
		t.Fatalf("updated = %+v, %v", current, err)
	}
	if pinned, err := manager.ResolveForOwner(ctx, "user", "user", "app", "1", "portable"); err != nil || pinned.BundlePath != personal.BundlePath {
		t.Fatalf("personal pin changed owner = %+v, %v", pinned, err)
	}
	if _, err := manager.ResolveForOwner(ctx, "other", "user", "app", "1", "portable"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("foreign owner pin = %v", err)
	}
	if err := manager.RegisterBundles(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Install(ctx, v2); !errors.Is(err, ErrManagedPlugin) {
		t.Fatalf("CLI re-enabled removed managed app = %v", err)
	}
	if listed, err := manager.ListFor(ctx, "other"); err != nil || len(listed) != 0 {
		t.Fatalf("removed managed app still listed = %+v, %v", listed, err)
	}
	if _, err := manager.ResolveFor(ctx, "other", "app", "", "portable"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("removed managed app resolves as new = %v", err)
	}
	if current, err := manager.ResolveFor(ctx, "user", "app", "", "portable"); err != nil || current.OwnerID != "user" {
		t.Fatalf("personal installation not retained = %+v, %v", current, err)
	}
	for _, version := range []string{"1", "2"} {
		pinned, err := manager.ResolveForOwner(ctx, "user", "", "app", version, "portable")
		if err != nil || pinned.OwnerID != "" || !pinned.Managed {
			t.Fatalf("removed managed pin %s = %+v, %v", version, pinned, err)
		}
		if _, err := os.Stat(filepath.Join(pinned.BundlePath, "server.py")); err != nil {
			t.Fatal(err)
		}
	}
	manager = New(cache, st)
	if err := manager.RegisterBundles(ctx, []string{v1}); err != nil {
		t.Fatal(err)
	}
	if restored, err := manager.ResolveFor(ctx, "user", "app", "", "portable"); err != nil || restored.BundlePath != first.BundlePath {
		t.Fatalf("re-enabled = %+v, %v", restored, err)
	}
}

func TestManagedBundlesMultipleTargets(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "brigade.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	manager := New(t.TempDir(), st)
	var sources []string
	for _, platform := range []string{"linux", "darwin"} {
		manifest := managedTestManifest("app", "1")
		manifest.Server.Type = "binary"
		manifest.Compatibility.Platforms = []string{platform}
		sources = append(sources, writeManagedTestBundle(t, manifest, map[string]string{"server.py": "#!/bin/sh\nexit 0\n"}))
	}
	if err := manager.RegisterBundles(ctx, sources); err != nil {
		t.Fatal(err)
	}
	if listed, err := manager.ListFor(ctx, "user"); err != nil || len(listed) != 2 {
		t.Fatalf("platform variants = %+v, %v", listed, err)
	}
	for _, platform := range []string{"linux", "darwin"} {
		item, err := manager.ResolveFor(ctx, "user", "app", "", platform+"-arm64")
		if err != nil || item.Target != platform+"-any" || !item.Managed {
			t.Fatalf("platform %s = %+v, %v", platform, item, err)
		}
	}
}

func TestManagedBundlesValidateAllBeforeRegistration(t *testing.T) {
	for _, name := range []string{"missing entry", "unsafe path", "missing cover", "invalid layout", "duplicate target", "remote source"} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			st, err := store.Open(filepath.Join(t.TempDir(), "brigade.db"), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			manager := New(t.TempDir(), st)
			old := writeManagedTestBundle(t, managedTestManifest("old", "1"), map[string]string{"server.py": "pass"})
			if err := manager.RegisterBundles(ctx, []string{old}); err != nil {
				t.Fatal(err)
			}
			good := writeManagedTestBundle(t, managedTestManifest("new", "1"), map[string]string{"server.py": "pass"})
			badManifest := managedTestManifest("bad", "1")
			files := map[string]string{"server.py": "pass"}
			switch name {
			case "missing entry":
				delete(files, "server.py")
			case "unsafe path":
				files["../escape"] = "unsafe"
			case "missing cover":
				badManifest.Meta.Brigade.Experience.Cover = "missing.png"
			case "invalid layout":
				badManifest.Meta.Brigade.Experience.Layout = "unknown"
			case "duplicate target":
				badManifest.Name, badManifest.Version = "new", "2"
			}
			bad := writeManagedTestBundle(t, badManifest, files)
			if name == "remote source" {
				bad = "https://example.com/app.mcpb"
			}
			if err := manager.RegisterBundles(ctx, []string{good, bad}); err == nil {
				t.Fatal("invalid set registered")
			}
			if _, err := st.GetPluginVariant(ctx, "", "new", "1", "portable"); !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("partial registration = %v", err)
			}
			if listed, err := manager.ListFor(ctx, "user"); err != nil || len(listed) != 1 || listed[0].ID != "old" {
				t.Fatalf("previous catalog changed = %+v, %v", listed, err)
			}
			staging, err := filepath.Glob(filepath.Join(manager.dir, ".install-*"))
			if err != nil || len(staging) != 0 {
				t.Fatalf("staging leaked = %v, %v", staging, err)
			}
		})
	}
}

func TestManifestExperienceLayout(t *testing.T) {
	for _, layout := range []string{"", "full", "split", "Full", "unknown"} {
		manifest := managedTestManifest("app", "1")
		manifest.Meta.Brigade.Experience.Layout = layout
		raw, err := json.Marshal(manifest)
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := ParseManifest(raw)
		valid := layout == "" || layout == "full" || layout == "split"
		if valid && (err != nil || parsed.Meta.Brigade.Experience.Layout != "split") {
			t.Fatalf("layout %q = %+v, %v", layout, parsed, err)
		}
		if !valid && err == nil {
			t.Fatalf("invalid layout %q accepted", layout)
		}
	}
}
