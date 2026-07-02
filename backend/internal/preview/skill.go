package preview

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
)

//go:embed skill/SKILL.md
var skillMD []byte

// skillRelPath — путь скилла относительно рабочей директории сессии. Claude Code
// подхватывает скиллы проекта из .claude/skills автоматически.
const skillRelPath = ".claude/skills/brigade-preview/SKILL.md"

// InstallSkill кладёт скилл brigade-preview в рабочую директорию сессии.
// Существующий файл не перезаписывается: пользовательская версия приоритетна.
func InstallSkill(cwd string) error {
	path := filepath.Join(cwd, filepath.FromSlash(skillRelPath))
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("preview: skill dir: %w", err)
	}
	if err := os.WriteFile(path, skillMD, 0o644); err != nil {
		return fmt.Errorf("preview: write skill: %w", err)
	}
	return nil
}
