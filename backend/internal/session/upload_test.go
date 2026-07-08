package session

import "testing"

// TestSanitizeUploadName проверяет, что имя файла не выносит запись за uploads/ (path
// traversal) и приводится к безопасному непустому виду.
func TestSanitizeUploadName(t *testing.T) {
	cases := map[string]string{
		"screenshot.png":       "screenshot.png",
		"../../etc/passwd":     "passwd",
		"/abs/path/report.pdf": "report.pdf",
		"my file (1).txt":      "my-file--1-.txt",
		"..":                   "file",
		"":                     "file",
		"...":                  "file",
	}
	for in, want := range cases {
		if got := sanitizeUploadName(in); got != want {
			t.Errorf("sanitizeUploadName(%q) = %q, want %q", in, got, want)
		}
	}
}
