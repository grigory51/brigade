package agent

import (
	"strings"
	"testing"

	"github.com/grigory51/brigade/backend/internal/store"
)

// TestManifest проверяет, что каждый агент запускается в обоих видах сессии и что состав
// runtime непротиворечив: манифест — единственный источник этих данных, и пустое поле
// обернулось бы неподнявшейся сессией уже в проде.
func TestManifest(t *testing.T) {
	kinds := []store.SessionKind{store.SessionKindCLI, store.SessionKindACP}
	for _, at := range List() {
		if at.ID == "" || at.Name == "" {
			t.Fatalf("агент без идентификатора или имени: %+v", at)
		}
		for _, kind := range kinds {
			if at.CommandFor(kind) == "" {
				t.Errorf("%s/%s: не задана команда запуска", at.ID, kind)
			}
			layers := at.LayersFor(kind)
			if len(layers) == 0 || layers[0] != LayerDaemon {
				t.Errorf("%s/%s: демон должен приезжать первым слоем: %+v", at.ID, kind, layers)
			}
			seen := map[string]bool{}
			for _, l := range layers {
				if l.Name == "" {
					t.Errorf("%s/%s: слой без имени", at.ID, kind)
				}
				if seen[l.Name] {
					t.Errorf("%s/%s: слой %q продублирован", at.ID, kind, l.Name)
				}
				seen[l.Name] = true
			}
		}
		// MCP-сервер brigade — только для ACP: в CLI его инструменты некому отрисовать.
		if at.McpServerScript != "" && !strings.HasPrefix(at.McpServerScript, RuntimeRoot+"/") {
			t.Errorf("%s: путь MCP-сервера вне runtime: %s", at.ID, at.McpServerScript)
		}
	}
}

// TestGetFallback: неизвестный агент не должен ронять спавн — сессии прежних версий и
// клиенты без agent_type получают дефолтного.
func TestGetFallback(t *testing.T) {
	if got := Get("no-such-agent"); got.ID != List()[0].ID {
		t.Fatalf("ожидался дефолтный агент, получен %q", got.ID)
	}
	if got := Get(Claude.ID); got.ID != Claude.ID {
		t.Fatalf("агент по идентификатору не найден: %q", got.ID)
	}
}

func TestLayerPaths(t *testing.T) {
	if got := LayerDaemon.BinPath(); got != RuntimeRoot+"/daemon/bin" {
		t.Fatalf("путь bin слоя: %s", got)
	}
	// Слой без исполняемых файлов не должен попадать в PATH.
	if got := LayerBrigadeMCP.BinPath(); got != "" {
		t.Fatalf("слой без bin вернул путь: %s", got)
	}
}
