package acp

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const maxGeneratedImageBytes = 10 << 20

// GeneratedImage — изображение, возвращённое инструментом агента как data URL.
type GeneratedImage struct {
	MIMEType string
	Data     []byte
}

// GeneratedImageFile — компактная ссылка на материализованный результат генерации.
type GeneratedImageFile struct {
	MIMEType string `json:"mimeType"`
	Path     string `json:"path"`
}

type generatedImageResult struct {
	Type   string               `json:"type"`
	Images []GeneratedImageFile `json:"images"`
}

// GeneratedImages извлекает data:image/...;base64 из JSON-результата инструмента без
// десериализации всей строки: payload изображения может занимать несколько мегабайт.
func GeneratedImages(result string) []GeneratedImage {
	const prefix = "data:image/"
	var images []GeneratedImage
	for offset := 0; ; {
		start := strings.Index(result[offset:], prefix)
		if start < 0 {
			return images
		}
		start += offset
		separator := strings.Index(result[start:], ";base64,")
		if separator < 0 {
			offset = start + len(prefix)
			continue
		}
		separator += start
		mimeType := result[start+len("data:") : separator]
		if mimeType != "image/png" && mimeType != "image/jpeg" && mimeType != "image/webp" {
			offset = separator + len(";base64,")
			continue
		}
		dataStart := separator + len(";base64,")
		dataEnd := dataStart
		for dataEnd < len(result) && isBase64(result[dataEnd]) {
			dataEnd++
		}
		encoded := result[dataStart:dataEnd]
		if encoded != "" && base64.StdEncoding.DecodedLen(len(encoded)) <= maxGeneratedImageBytes {
			if data, err := base64.StdEncoding.DecodeString(encoded); err == nil {
				images = append(images, GeneratedImage{MIMEType: mimeType, Data: data})
			}
		}
		offset = dataEnd
	}
}

// MaterializeGeneratedImages заменяет встроенные data URL небольшими ссылками на файлы
// workspace. Generic tool-result наружу не передаётся: он может быть произвольного размера.
func MaterializeGeneratedImages(cwd, toolCallID, result string) (string, bool, error) {
	images := GeneratedImages(result)
	if len(images) == 0 {
		return "", false, nil
	}
	dir := filepath.Join(cwd, "generated-images")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", true, err
	}
	hash := sha256.Sum256([]byte(toolCallID))
	prefix := hex.EncodeToString(hash[:8])
	files := make([]GeneratedImageFile, 0, len(images))
	for index, image := range images {
		ext := ".png"
		if image.MIMEType == "image/jpeg" {
			ext = ".jpg"
		} else if image.MIMEType == "image/webp" {
			ext = ".webp"
		}
		name := fmt.Sprintf("%s-%d%s", prefix, index+1, ext)
		if err := os.WriteFile(filepath.Join(dir, name), image.Data, 0o600); err != nil {
			return "", true, err
		}
		files = append(files, GeneratedImageFile{MIMEType: image.MIMEType, Path: "generated-images/" + name})
	}
	encoded, err := json.Marshal(generatedImageResult{Type: "generated_images", Images: files})
	return string(encoded), true, err
}

// GeneratedImageFiles читает компактный результат, созданный MaterializeGeneratedImages.
func GeneratedImageFiles(result string) []GeneratedImageFile {
	var decoded generatedImageResult
	if json.Unmarshal([]byte(result), &decoded) != nil || decoded.Type != "generated_images" {
		return nil
	}
	return decoded.Images
}

func isBase64(c byte) bool {
	return c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '+' || c == '/' || c == '='
}
