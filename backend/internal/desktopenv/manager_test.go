package desktopenv

import (
	"context"
	"testing"
)

func TestNormalizeRemoteURL(t *testing.T) {
	for _, test := range []struct {
		input string
		want  string
		valid bool
	}{
		{"https://brigade.example.com/", "https://brigade.example.com", true},
		{"http://127.0.0.1:8080", "http://127.0.0.1:8080", true},
		{"http://brigade.example.com", "", false},
		{"https://user:pass@brigade.example.com", "", false},
		{"https://brigade.example.com/path", "", false},
	} {
		got, err := normalizeRemoteURL(test.input)
		if test.valid && (err != nil || got != test.want) {
			t.Fatalf("normalize %q = %q, %v", test.input, got, err)
		}
		if !test.valid && err == nil {
			t.Fatalf("normalize %q unexpectedly succeeded", test.input)
		}
	}
}

func TestShouldProxy(t *testing.T) {
	for path, want := range map[string]bool{
		"/brigade.v1.SessionService/List":             true,
		"/brigade.v1.DesktopService/ListEnvironments": false,
		"/api/ag-ui/run":                              true,
		"/ws/terminal/session":                        true,
		"/settings":                                   false,
	} {
		if got := shouldProxy(path); got != want {
			t.Errorf("shouldProxy(%q) = %v, want %v", path, got, want)
		}
	}
}

func TestDetachResourcesRotatesContext(t *testing.T) {
	old, cancel := context.WithCancel(context.Background())
	m := &Manager{resourceContext: old, resourceCancel: cancel}
	m.detachResourcesLocked()
	select {
	case <-old.Done():
	default:
		t.Fatal("old resource context was not canceled")
	}
	select {
	case <-m.resourceContext.Done():
		t.Fatal("new resource context is already canceled")
	default:
	}
}
