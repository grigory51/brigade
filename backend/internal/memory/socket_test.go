package memory

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestSSHAgentSocketPathIsShortAndOutsideMemoryTree(t *testing.T) {
	service := NewService(filepath.Join(t.TempDir(), "Application Support", "Brigade", "memory"), nil, nil)
	defer service.Close()
	userID := "f6cee440-cb1d-44ac-8479-f71c13585f53"
	path := service.agentSocketPath(userID)
	if len(path) >= 100 {
		t.Fatalf("путь Unix-сокета всё ещё слишком длинный: %d %s", len(path), path)
	}
	if strings.Contains(path, userID) || strings.HasPrefix(path, service.baseDir) {
		t.Fatalf("runtime-сокет попал в дерево памяти: %s", path)
	}
}
