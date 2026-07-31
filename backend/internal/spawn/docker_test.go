package spawn

import (
	"testing"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
)

func TestRuntimeMountsCurrent(t *testing.T) {
	expected := []mount.Mount{
		{Type: mount.TypeVolume, Source: "brigade-rt-daemon-new", Target: "/opt/brigade-runtime/daemon"},
		{Type: mount.TypeVolume, Source: "brigade-rt-codex-new", Target: "/opt/brigade-runtime/codex"},
	}
	current := []container.MountPoint{
		{Type: mount.TypeVolume, Name: "brigade-rt-daemon-new", Destination: "/opt/brigade-runtime/daemon"},
		{Type: mount.TypeVolume, Name: "brigade-rt-codex-new", Destination: "/opt/brigade-runtime/codex"},
	}
	if !runtimeMountsCurrent(current, expected) {
		t.Fatal("актуальные runtime mounts признаны устаревшими")
	}
	current[1].Name = "brigade-rt-codex-old"
	if runtimeMountsCurrent(current, expected) {
		t.Fatal("старый Codex runtime не потребовал пересоздания контейнера")
	}
}
