package connectsvc

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestWorkspaceListPage(t *testing.T) {
	tests := []struct {
		name     string
		pageSize int32
		token    string
		wantSize int
		wantOff  int
		wantPage bool
		wantErr  bool
	}{
		{name: "full listing"},
		{name: "first page", pageSize: 500, wantSize: 500, wantPage: true},
		{name: "offset", pageSize: 1, token: "42", wantSize: 1, wantOff: 42, wantPage: true},
		{name: "negative size", pageSize: -1, wantErr: true},
		{name: "oversized page", pageSize: 501, wantErr: true},
		{name: "token without pagination", token: "1", wantErr: true},
		{name: "non-decimal token", pageSize: 1, token: "1a", wantErr: true},
		{name: "offset cap", pageSize: 1, token: "1000001", wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			size, offset, paginated, err := workspaceListPage(test.pageSize, test.token)
			if (err != nil) != test.wantErr {
				t.Fatalf("workspaceListPage() error = %v", err)
			}
			if err == nil && (size != test.wantSize || offset != test.wantOff || paginated != test.wantPage) {
				t.Fatalf("workspaceListPage() = (%d, %d, %t), want (%d, %d, %t)", size, offset, paginated, test.wantSize, test.wantOff, test.wantPage)
			}
		})
	}
}

func TestListWorkspaceDirectoryPagination(t *testing.T) {
	directoryPath := t.TempDir()
	for i := range 1201 {
		if err := os.WriteFile(filepath.Join(directoryPath, fmt.Sprintf("file-%04d", i)), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	root, err := os.OpenRoot(directoryPath)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	seen := make(map[string]struct{}, 1201)
	token := ""
	for page, wantEntries := range []int{500, 500, 201} {
		dir, err := root.Open(".")
		if err != nil {
			t.Fatal(err)
		}
		_, offset, paginated, err := workspaceListPage(500, token)
		if err != nil {
			dir.Close()
			t.Fatal(err)
		}
		response, err := listWorkspaceDirectory(context.Background(), dir, ".", 500, offset, paginated)
		dir.Close()
		if err != nil {
			t.Fatal(err)
		}
		if len(response.Entries) != wantEntries {
			t.Fatalf("page %d returned %d entries, want %d", page, len(response.Entries), wantEntries)
		}
		for _, entry := range response.Entries {
			if _, exists := seen[entry.Name]; exists {
				t.Fatalf("duplicate entry %q", entry.Name)
			}
			seen[entry.Name] = struct{}{}
		}
		token = response.NextPageToken
	}
	if token != "" || len(seen) != 1201 {
		t.Fatalf("last page token=%q entries=%d", token, len(seen))
	}

	dir, err := root.Open(".")
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := listWorkspaceDirectory(ctx, dir, ".", 500, 0, true); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled listing error: %v", err)
	}
}
