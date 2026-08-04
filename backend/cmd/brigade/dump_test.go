package main

import (
	"strings"
	"testing"
)

func TestDebugPreviewLimitsPayload(t *testing.T) {
	value := strings.Repeat("x", debugPayloadLimit+1)
	got := debugPreview(value)
	if len(got) != debugPayloadLimit+len("…") || !strings.HasSuffix(got, "…") {
		t.Fatalf("debugPreview() returned %d bytes without truncation marker", len(got))
	}
}
