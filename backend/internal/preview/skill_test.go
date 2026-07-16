package preview

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestInstallSkillValidPluginFormat: InstallSkill пишет marketplace-манифест и settings.json в
// формате, который принимает Claude Code (enabledPlugins — только ключ "<плагин>@<marketplace>":
// true; источник — в extraKnownMarketplaces). Прежний невалидный ключ "brigade" (объект с source)
// мигрируется прочь; чужие ключи settings сохраняются.
func TestInstallSkillValidPluginFormat(t *testing.T) {
	cwd := t.TempDir()

	// Предзаданный settings.json: чужой ключ + СТАРЫЙ невалидный формат brigade + прежние
	// per-marketplace-записи (константный brigade-local) — проверяем миграцию и очистку.
	old := `{"foreign":true,` +
		`"enabledPlugins":{"brigade":{"x":1},"brigade@brigade-local":true},` +
		`"extraKnownMarketplaces":{"brigade-local":{"source":{"source":"directory","path":"x"}},"other-mp":{"source":{}}}}`
	if err := os.MkdirAll(filepath.Join(cwd, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwd, settingsRel), []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}

	const mpID = "brigade-testsess"
	if err := InstallSkill(cwd, mpID); err != nil {
		t.Fatalf("InstallSkill: %v", err)
	}

	if _, err := os.Stat(filepath.Join(cwd, pluginMarketplaceRel)); err != nil {
		t.Errorf("marketplace.json не создан: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(cwd, settingsRel))
	if err != nil {
		t.Fatal(err)
	}
	var s map[string]any
	if err := json.Unmarshal(data, &s); err != nil {
		t.Fatalf("settings.json не парсится: %v", err)
	}

	enabled, _ := s["enabledPlugins"].(map[string]any)
	if enabled["brigade@"+mpID] != true {
		t.Errorf("нет enabledPlugins[brigade@%s]=true: %v", mpID, enabled)
	}
	if _, bad := enabled["brigade"]; bad {
		t.Error("остался прежний невалидный ключ enabledPlugins.brigade")
	}
	if _, stale := enabled["brigade@brigade-local"]; stale {
		t.Error("прежний enabledPlugins[brigade@brigade-local] не очищен")
	}
	markets, _ := s["extraKnownMarketplaces"].(map[string]any)
	if _, ok := markets[mpID].(map[string]any); !ok {
		t.Errorf("нет extraKnownMarketplaces[%s]: %v", mpID, markets)
	}
	if _, stale := markets["brigade-local"]; stale {
		t.Error("прежний extraKnownMarketplaces[brigade-local] не очищен")
	}
	if _, ok := markets["other-mp"]; !ok {
		t.Error("чужой marketplace other-mp затёрт")
	}
	if s["foreign"] != true {
		t.Error("чужой ключ settings затёрт")
	}
}
