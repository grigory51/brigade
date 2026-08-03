package acp

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
)

func TestGeneratedImages(t *testing.T) {
	png := []byte("fake-png")
	encoded := base64.StdEncoding.EncodeToString(png)
	result := `{"result":{"image_url":"data:image/png;base64,` + encoded + `","output_hint":"image"}}`
	images := GeneratedImages(result)
	if len(images) != 1 || images[0].MIMEType != "image/png" || string(images[0].Data) != string(png) {
		t.Fatalf("GeneratedImages: %+v", images)
	}
	dir := t.TempDir()
	result, found, err := MaterializeGeneratedImages(dir, "tool-1", result)
	if err != nil || !found {
		t.Fatalf("MaterializeGeneratedImages: found=%v err=%v", found, err)
	}
	files := GeneratedImageFiles(result)
	if len(files) != 1 || files[0].MIMEType != "image/png" {
		t.Fatalf("GeneratedImageFiles: %+v", files)
	}
	data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(files[0].Path)))
	if err != nil || string(data) != string(png) {
		t.Fatalf("materialized image: %q err=%v", data, err)
	}

	images = GeneratedImages(`{"content":{"type":"image","mimeType":"image/png","data":"` + encoded + `"}}`)
	if len(images) != 1 || images[0].MIMEType != "image/png" || string(images[0].Data) != string(png) {
		t.Fatalf("image content block: %+v", images)
	}
}
