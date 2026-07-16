package preview

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

//go:embed skill/SKILL.md
var skillMD []byte

//go:embed skill/note/SKILL.md
var noteSkillMD []byte

//go:embed skill/plugin.json
var pluginJSON []byte

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
	settingsRel = ".claude/settings.json"
	// Имя плагина; enabledPlugins-ключ = pluginName@marketplaceID. Сам marketplaceID —
	// НЕ константа, а уникальный на сессию (см. InstallSkill): Claude Code кеширует локальный
	// marketplace глобально по (ID, версия) и пинит source-path на каталог ПЕРВОЙ сессии,
	// зарегистрировавшей этот ID. При константном ID новые сессии получали бы старый
	// закешированный список скиллов из чужого каталога (переименование memory→note не долетало).
	pluginName = "brigade"
	// legacySkillDirRel — прежнее место установки (обычный проектный скилл /brigade-preview).
	// Удаляется, чтобы он не дублировал скилл плагина в slash-меню.
	legacySkillDirRel = ".claude/skills/brigade-preview"
	// legacyMemorySkillRel — прежний скилл /brigade:memory (переименован в /brigade:note, т.к.
	// /memory конфликтует со встроенной командой Claude Code). Удаляется, чтобы не дублировался.
	legacyMemorySkillRel = ".claude/plugins/brigade/skills/memory"
)

// InstallSkill раскладывает per-session плагин brigade со скиллами preview и note в рабочую
// директорию сессии и включает его в проектном .claude/settings.json (enabledPlugins).
// Результат — вызовы /brigade:preview и /brigade:note. marketplaceID УНИКАЛЕН на сессию
// (передаётся вызывающим как "brigade-<sessionID>") — иначе Claude Code кеширует marketplace
// глобально по константному ID и не перечитывает скиллы. Идемпотентна (перезапись безопасна);
// мерж settings.json сохраняет чужие ключи. Удаляет прежний обычный скилл brigade-preview.
func InstallSkill(cwd, marketplaceID string) error {
	join := func(rel string) string { return filepath.Join(cwd, filepath.FromSlash(rel)) }

	if err := writeFile(join(pluginManifestRel), pluginJSON); err != nil {
		return err
	}
	// Манифест marketplace рядом с plugin.json: каталог плагина регистрируется как локальный
	// directory-marketplace (см. enablePlugin), иначе Claude Code не может включить плагин.
	// Имя marketplace ДОЛЖНО совпадать с ключом в extraKnownMarketplaces (marketplaceID),
	// поэтому генерируем манифест с per-session именем, а не встраиваем статикой.
	market := fmt.Sprintf(
		`{"name":%q,"owner":{"name":"Brigade"},"plugins":[{"name":%q,"source":"./"}]}`,
		marketplaceID, pluginName)
	if err := writeFile(join(pluginMarketplaceRel), []byte(market)); err != nil {
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
	if err := enablePlugin(join(settingsRel), marketplaceID); err != nil {
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
func enablePlugin(path, marketplaceID string) error {
	settings := map[string]any{}
	if data, err := os.ReadFile(path); err == nil {
		// Повреждённый/пустой файл перезапишем валидным; иначе сохраняем существующие ключи.
		_ = json.Unmarshal(data, &settings)
	}

	markets, _ := settings["extraKnownMarketplaces"].(map[string]any)
	if markets == nil {
		markets = map[string]any{}
	}
	// Сносим прежние brigade-маркетплейсы (константный "brigade-local" и любые прошлые
	// per-session ID) — иначе в settings.json копились бы устаревшие записи.
	for k := range markets {
		if strings.HasPrefix(k, "brigade-") {
			delete(markets, k)
		}
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
	// Сносим прежние brigade@<...>-ключи (в т.ч. brigade@brigade-local), оставляя чужие.
	for k := range enabled {
		if strings.HasPrefix(k, pluginName+"@") {
			delete(enabled, k)
		}
	}
	enabled[pluginName+"@"+marketplaceID] = true
	settings["enabledPlugins"] = enabled

	out, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("preview: marshal settings: %w", err)
	}
	return writeFile(path, out)
}
