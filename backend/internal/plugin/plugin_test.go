package plugin

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestManifestAndSafeExtraction(t *testing.T) {
	raw := []byte(`{"manifest_version":"0.3","name":"cad","version":"1.0.0","server":{"type":"binary","entry_point":"server/cad"},"_meta":{"brigade":{"experience":{"entry_tool":"cad.open"}}}}`)
	if _, err := ParseManifest(raw); err != nil {
		t.Fatal(err)
	}
	unsafeVersion := []byte(`{"manifest_version":"0.3","name":"cad","version":"..","server":{"type":"binary","entry_point":"server/cad"},"_meta":{"brigade":{"experience":{"entry_tool":"cad.open"}}}}`)
	if _, err := ParseManifest(unsafeVersion); err == nil {
		t.Fatal("unsafe version must be rejected")
	}
	var data bytes.Buffer
	zw := zip.NewWriter(&data)
	w, _ := zw.Create("../escape")
	_, _ = w.Write([]byte("x"))
	_ = zw.Close()
	zr, _ := zip.NewReader(bytes.NewReader(data.Bytes()), int64(data.Len()))
	dir := t.TempDir()
	if err := extract(zr, dir); err == nil {
		t.Fatal("path traversal must be rejected")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "escape")); !os.IsNotExist(err) {
		t.Fatal("archive escaped destination")
	}
}

func TestReadCover(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "ui"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ui", "cover.svg"), []byte("<svg/>"), 0o644); err != nil {
		t.Fatal(err)
	}
	manifest := Manifest{}
	manifest.Meta.Brigade.Experience.Cover = "ui/cover.svg"
	data, mimeType, err := ReadCover(dir, manifest)
	if err != nil || string(data) != "<svg/>" || mimeType != "image/svg+xml" {
		t.Fatalf("ReadCover() = %q, %q, %v", data, mimeType, err)
	}
}
