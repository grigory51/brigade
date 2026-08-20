package spawn

import (
	"testing"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
)

func TestAgentNetworkConfig(t *testing.T) {
	s := &DockerSpawner{agentNetwork: "brigade-agents"}
	if got := string(s.netMode()); got != s.agentNetwork {
		t.Fatalf("NetworkMode = %q, want %q", got, s.agentNetwork)
	}
	config := s.networkingConfig()
	if config == nil || config.EndpointsConfig[s.agentNetwork] == nil {
		t.Fatalf("EndpointsConfig = %v, want %q", config, s.agentNetwork)
	}
}

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

func TestStaleRuntimeVolume(t *testing.T) {
	for name, want := range map[string]bool{
		"brigade-rt-codex-old": false,
		"brigade-rt-codex-new": true,
		"brigade-claude-old":   false,
		"project-data":         false,
	} {
		if got := staleRuntimeVolume(name, "old"); got != want {
			t.Errorf("staleRuntimeVolume(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestHardenAgentContainer(t *testing.T) {
	hostCfg := &container.HostConfig{}
	hardenAgentContainer(hostCfg)
	if len(hostCfg.CapDrop) != 1 || hostCfg.CapDrop[0] != "ALL" {
		t.Fatalf("CapDrop = %v, want [ALL]", hostCfg.CapDrop)
	}
	if len(hostCfg.SecurityOpt) != 1 || hostCfg.SecurityOpt[0] != "no-new-privileges=true" {
		t.Fatalf("SecurityOpt = %v, want no-new-privileges", hostCfg.SecurityOpt)
	}
}
