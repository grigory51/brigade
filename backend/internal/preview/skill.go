package preview

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

//go:embed skill/SKILL.md
var skillMD []byte

//go:embed skill/note/SKILL.md
var noteSkillMD []byte

//go:embed skill/plugin.json
var pluginJSON []byte

//go:embed skill/marketplace.json
var marketplaceJSON []byte

// Пути плагина brigade относительно рабочей директории сессии. Скилл ставится ПЕР-СЕССИЙНО
// как локальный плагин (namespace /brigade:preview) и включается проектным
// .claude/settings.json. Каталог плагина выступает и marketplace'ом (в нём — и plugin.json, и
// marketplace.json): Claude Code включает плагин только ключом "<плагин>@<marketplace>", а
// сам источник объявляется в extraKnownMarketplaces (см. enablePlugin).
const (
	PluginDirRel         = ".claude/plugins/brigade"
	pluginManifestRel    = ".claude/plugins/brigade/.claude-plugin/plugin.json"
	pluginMarketplaceRel = ".claude/plugins/brigade/.claude-plugin/marketplace.json"
	pluginSkillRel       = ".claude/plugins/brigade/skills/preview/SKILL.md"
	pluginNoteRel        = ".claude/plugins/brigade/skills/note/SKILL.md"
	settingsRel          = ".claude/settings.json"
	// Идентификатор локального marketplace и имя плагина: enabledPlugins-ключ = pluginName@marketplaceID.
	marketplaceID = "brigade-local"
	pluginName    = "brigade"
	// legacySkillDirRel — прежнее место установки (обычный проектный скилл /brigade-preview).
	// Удаляется, чтобы он не дублировал скилл плагина в slash-меню.
	legacySkillDirRel = ".claude/skills/brigade-preview"
	// legacyMemorySkillRel — прежний скилл /brigade:memory (переименован в /brigade:note, т.к.
	// /memory конфликтует со встроенной командой Claude Code). Удаляется, чтобы не дублировался.
	legacyMemorySkillRel = ".claude/plugins/brigade/skills/memory"
)

// InstallSkill раскладывает per-session плагин brigade со скиллами preview и memory в
// рабочую директорию сессии и включает его в проектном .claude/settings.json
// (enabledPlugins). Результат — вызовы /brigade:preview и /brigade:memory. Идемпотентна
// (перезапись безопасна); мерж settings.json сохраняет чужие ключи. Удаляет прежний
// обычный скилл brigade-preview из этой же директории.
func InstallSkill(cwd string) error {
	join := func(rel string) string { return filepath.Join(cwd, filepath.FromSlash(rel)) }

	if err := writeFile(join(pluginManifestRel), pluginJSON); err != nil {
		return err
	}
	// Манифест marketplace рядом с plugin.json: каталог плагина регистрируется как локальный
	// directory-marketplace (см. enablePlugin), иначе Claude Code не может включить плагин.
	if err := writeFile(join(pluginMarketplaceRel), marketplaceJSON); err != nil {
		return err
	}
	if err := writeFile(join(pluginSkillRel), skillMD); err != nil {
		return err
	}
	// Второй скилл того же плагина brigade — личная память (/brigade:note). Скиллы
	// авто-дискаверятся по директориям, enabledPlugins ниже включает плагин целиком.
	// ponytail: ставится вместе с preview-скиллом; если память не сконфигурирована
	// (memory.remote пуст), сам вызов CreateMemoryNote вернёт failed_precondition.
	if err := writeFile(join(pluginNoteRel), noteSkillMD); err != nil {
		return err
	}
	if err := enablePlugin(join(settingsRel)); err != nil {
		return err
	}
	// Убираем прежний обычный скилл, чтобы /brigade-preview не дублировал /brigade:preview.
	_ = os.RemoveAll(join(legacySkillDirRel))
	// Сносим прежний скилл /brigade:memory (переименован в /brigade:note) — иначе в старых
	// workspace'ах он остался бы рядом дублирующим орфаном.
	_ = os.RemoveAll(join(legacyMemorySkillRel))
	return nil
}

func writeFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("preview: mkdir %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("preview: write %s: %w", path, err)
	}
	return nil
}

// enablePlugin регистрирует локальный marketplace и включает плагин brigade в проектном
// .claude/settings.json, сохраняя прочие ключи (мерж). Формат строго по контракту Claude Code:
// источник объявляется в extraKnownMarketplaces (directory → каталог плагина с
// .claude-plugin/marketplace.json), а enabledPlugins принимает ТОЛЬКО ключ вида
// "<плагин>@<marketplace>": true. Прежний невалидный ключ ("brigade" с объектом source), из-за
// которого Claude Code ругался "enabledPlugins.brigade: Invalid input", удаляется.
func enablePlugin(path string) error {
	settings := map[string]any{}
	if data, err := os.ReadFile(path); err == nil {
		// Повреждённый/пустой файл перезапишем валидным; иначе сохраняем существующие ключи.
		_ = json.Unmarshal(data, &settings)
	}

	markets, _ := settings["extraKnownMarketplaces"].(map[string]any)
	if markets == nil {
		markets = map[string]any{}
	}
	markets[marketplaceID] = map[string]any{
		"source": map[string]any{
			"source": "directory",
			"path":   "./" + PluginDirRel,
		},
	}
	settings["extraKnownMarketplaces"] = markets

	enabled, _ := settings["enabledPlugins"].(map[string]any)
	if enabled == nil {
		enabled = map[string]any{}
	}
	delete(enabled, pluginName) // снести прежний невалидный формат
	enabled[pluginName+"@"+marketplaceID] = true
	settings["enabledPlugins"] = enabled

	out, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("preview: marshal settings: %w", err)
	}
	return writeFile(path, out)
}
