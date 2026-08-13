package connectsvc

import "testing"

func TestAgentOutdated(t *testing.T) {
	for _, test := range []struct {
		brigade string
		agent   string
		want    bool
	}{
		{"v0.54.0", "v0.52.0", true},
		{"v0.54.0", "v0.54.0", false},
		{"v0.54.0", "", false},
		{"dev", "v0.54.0", false},
	} {
		if got := agentOutdated(test.brigade, test.agent); got != test.want {
			t.Fatalf("agentOutdated(%q, %q) = %v, want %v", test.brigade, test.agent, got, test.want)
		}
	}
}
