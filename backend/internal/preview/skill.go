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

//go:embed skill/plugin.json
var pluginJSON []byte

// Пути плагина brigade относительно рабочей директории сессии. Скилл ставится ПЕР-СЕССИЙНО
// как плагин (namespace /brigade:preview) и включается проектным .claude/settings.json:
// обычный skills-dir плагин авто-грузится только из личного ~/.claude, а он общий на
// пользователя (туда класть нельзя); enabledPlugins в проектном settings.json Agent SDK
// читает по cwd сессии (см. claude-agent-acp).
const (
	PluginDirRel      = ".claude/plugins/brigade"
	pluginManifestRel = ".claude/plugins/brigade/.claude-plugin/plugin.json"
	pluginSkillRel    = ".claude/plugins/brigade/skills/preview/SKILL.md"
	settingsRel       = ".claude/settings.json"
	// legacySkillDirRel — прежнее место установки (обычный проектный скилл /brigade-preview).
	// Удаляется, чтобы он не дублировал скилл плагина в slash-меню.
	legacySkillDirRel = ".claude/skills/brigade-preview"
)

// InstallSkill раскладывает per-session плагин brigade со скиллом preview в рабочую
// директорию сессии и включает его в проектном .claude/settings.json (enabledPlugins).
// Результат — вызов /brigade:preview. Идемпотентна (перезапись безопасна); мерж settings.json
// сохраняет чужие ключи. Удаляет прежний обычный скилл brigade-preview из этой же директории.
func InstallSkill(cwd string) error {
	join := func(rel string) string { return filepath.Join(cwd, filepath.FromSlash(rel)) }

	if err := writeFile(join(pluginManifestRel), pluginJSON); err != nil {
		return err
	}
	if err := writeFile(join(pluginSkillRel), skillMD); err != nil {
		return err
	}
	if err := enablePlugin(join(settingsRel)); err != nil {
		return err
	}
	// Убираем прежний обычный скилл, чтобы /brigade-preview не дублировал /brigade:preview.
	_ = os.RemoveAll(join(legacySkillDirRel))
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

// enablePlugin добавляет brigade в enabledPlugins проектного settings.json, сохраняя прочие
// настройки (мерж). directory-source — относительный путь плагина в проекте.
func enablePlugin(path string) error {
	settings := map[string]any{}
	if data, err := os.ReadFile(path); err == nil {
		// Повреждённый/пустой файл перезапишем валидным; иначе сохраняем существующие ключи.
		_ = json.Unmarshal(data, &settings)
	}
	enabled, _ := settings["enabledPlugins"].(map[string]any)
	if enabled == nil {
		enabled = map[string]any{}
	}
	enabled["brigade"] = map[string]any{
		"source": map[string]any{
			"source": "directory",
			"path":   PluginDirRel,
		},
	}
	settings["enabledPlugins"] = enabled

	out, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("preview: marshal settings: %w", err)
	}
	return writeFile(path, out)
}
