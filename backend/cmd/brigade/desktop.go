package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/grigory51/brigade/backend/internal/acp"
	"github.com/grigory51/brigade/backend/internal/desktopenv"
)

// Десктоп-режим: `brigade desktop` — «в один клик» на локальной машине. Готовит
// пер-пользовательский каталог данных и конфиг (со стабильным сгенерированным jwt.secret и
// абсолютными путями), обогащает PATH (GUI-процесс из Finder не наследует shell-PATH и не
// видит claude/claude-agent-acp), поднимает обычный сервер (runServer) в фоне и открывает
// нативное окно на localhost. Показ окна — платформенный (см. window_webview.go / window_browser.go).

// desktopAddr — локальный адрес десктоп-сервера. Только петля: приложение персональное.
const desktopAddr = "127.0.0.1:8787"

// desktopMode включает десктоп-специфику в runServer (авто-логин сид-пользователя через
// /desktop/auth). Выставляется runDesktop до старта сервера; в серверном режиме остаётся false.
var desktopMode bool
var desktopEnvironments *desktopenv.Manager

// desktopRuntimePath — файл настроек режима исполнения сессий (local|docker и
// docker-контекст), который правится из интерфейса приложения. Пуст в серверной
// инсталляции: там режим задаётся конфигом и из UI не меняется.
var desktopRuntimePath string

// desktopAgentImage — образ агента для docker-режима приложения. Локально собранного
// образа на машине пользователя нет, поэтому берём опубликованный. Подставляется, когда
// agent_image в конфиге не задан: конфиг создаётся один раз и у ранних установок этого
// поля нет.
const desktopAgentImage = "ghcr.io/grigory51/brigade-agent:latest"

// runDesktop — точка входа подкоманды desktop.
func runDesktop() {
	appDir, err := desktopAppDir()
	if err != nil {
		log.Fatalf("brigade desktop: app dir: %v", err)
	}
	cfgPath := filepath.Join(appDir, "config.yaml")
	_, configErr := os.Stat(cfgPath)
	newInstall := errors.Is(configErr, os.ErrNotExist)
	if err := ensureDesktopConfig(appDir, cfgPath); err != nil {
		log.Fatalf("brigade desktop: config: %v", err)
	}
	desktopEnvironments, err = desktopenv.New(filepath.Join(appDir, "environments.json"), newInstall)
	if err != nil {
		log.Fatalf("brigade desktop: environments: %v", err)
	}
	// Режим исполнения сессий правится из настроек приложения — отдельным файлом, чтобы не
	// переписывать пользовательский config.yaml (и не терять его комментарии).
	desktopRuntimePath = filepath.Join(appDir, "runtime.json")
	// Подхватывает и существующие config.yaml, созданные до появления agent_home_dir.
	_ = os.Setenv("BRIGADE_AGENT_HOME_DIR", filepath.Join(appDir, "agent-home"))
	_ = os.Setenv("BRIGADE_PLUGINS_DIR", filepath.Join(appDir, "plugins"))
	enrichPATH()

	// Стартуем с /desktop/auth: ручка ставит сессионные cookie сид-пользователя и редиректит на
	// SPA — приложение открывается сразу залогиненным, без экрана входа (локальный однопользоват.).
	url := "http://" + desktopAddr + "/desktop/auth"
	desktopMode = true
	// Если порт уже слушают, не запускаем параллельный npm update из второго app-процесса.
	if addrInUse(desktopAddr) {
		showWindow(url, "Brigade")
		return
	}
	resources := bundledResources()
	if err := ensureDesktopAgentRuntime(appDir, resources); err != nil {
		log.Fatalf("brigade desktop: agent runtime: %v", err)
	}
	prependBundledTools(appDir, resources)
	go runServer(cfgPath)
	if !waitReady(desktopAddr, 15*time.Second) {
		log.Fatalf("brigade desktop: сервер не поднялся за отведённое время")
	}
	go desktopEnvironments.Restore(context.Background())
	showWindow(url, "Brigade")
}

// desktopAppDir возвращает каталог данных приложения (<UserConfigDir>/Brigade; на macOS —
// ~/Library/Application Support/Brigade) и создаёт его.
func desktopAppDir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(base, "Brigade")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

// ensureDesktopConfig создаёт config.yaml при первом запуске: mode=local, локальный addr,
// абсолютные пути данных под appDir и сгенерированный стабильный jwt.secret. Существующий
// файл не трогает — секрет (он же ключ шифрования секретов в БД) обязан быть стабильным, а
// правки пользователя (в т.ч. mode: docker) — сохраняться.
func ensureDesktopConfig(appDir, cfgPath string) error {
	if _, err := os.Stat(cfgPath); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	secret, err := randomSecret()
	if err != nil {
		return err
	}
	yaml := fmt.Sprintf(`# Конфиг локального Brigade.app. Правьте под себя; секрет и пути стабильны.
# Режим исполнения сессий (local | docker) переключается в приложении: Настройки → Среда
# агента. Выбор хранится рядом, в runtime.json, и перекрывает mode отсюда.
mode: local
# Образ агента для docker-режима: опубликованный, локально его собирать не нужно —
# brigade подтянет его сам при первом запуске в этом режиме.
agent_image: "ghcr.io/grigory51/brigade-agent:latest"
addr: %q
sqlite_path: %q
jwt:
  # Сгенерирован один раз. НЕ меняйте: это ещё и ключ шифрования секретов в БД.
  secret: %q
  access_ttl: "15m"
  refresh_ttl: "720h"
seed:
  username: "admin"
  password: "admin"
work_dir: %q
agent_home_dir: %q
plugins_dir: %q
preview:
  enabled: true
  mode: "subdomain"
  domain: "localhost"
  scheme: "http"
memory:
  dir: %q
`,
		desktopAddr,
		filepath.Join(appDir, "brigade.db"),
		secret,
		filepath.Join(appDir, "workspace"),
		filepath.Join(appDir, "agent-home"),
		filepath.Join(appDir, "plugins"),
		filepath.Join(appDir, "memory"),
	)
	if err := os.WriteFile(cfgPath, []byte(yaml), 0o600); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	log.Printf("brigade desktop: создан конфиг %s", cfgPath)
	return nil
}

// randomSecret возвращает 32 случайных байта в hex — стабильный секрет для подписи JWT и
// шифрования секретных полей БД.
func randomSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("random secret: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// enrichPATH восстанавливает пользовательский PATH для GUI-запуска. Приложение, открытое из
// Finder, получает урезанный PATH (/usr/bin:/bin:/usr/sbin:/sbin) и не находит claude /
// claude-agent-acp (нужны в local-режиме). Забираем PATH из login-shell и добавляем типовые
// каталоги менеджеров пакетов. Спавнер отдаёт os.Environ() дочерним процессам, поэтому
// os.Setenv здесь долетает до агентов.
func enrichPATH() {
	var parts []string
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/zsh"
	}
	if out, err := exec.Command(shell, "-lc", `printf %s "$PATH"`).Output(); err == nil {
		if p := strings.TrimSpace(string(out)); p != "" {
			parts = append(parts, strings.Split(p, ":")...)
		}
	}
	parts = append(parts, os.Getenv("PATH"))
	parts = append(parts, "/opt/homebrew/bin", "/usr/local/bin")

	seen := map[string]bool{}
	var uniq []string
	for _, p := range parts {
		for _, d := range strings.Split(p, ":") {
			if d != "" && !seen[d] {
				seen[d] = true
				uniq = append(uniq, d)
			}
		}
	}
	_ = os.Setenv("PATH", strings.Join(uniq, ":"))
}

func bundledResources() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	return filepath.Join(filepath.Dir(exe), "..", "Resources")
}

// ensureDesktopAgentRuntime ставит и обновляет Claude, Codex и ACP-адаптеры в каталоге
// данных пользователя. При ошибке обновления существующий полный runtime остаётся доступен;
// первый запуск без всех четырёх команд завершается ошибкой.
func ensureDesktopAgentRuntime(appDir, resources string) error {
	if resources == "" {
		return nil
	}
	manifestPath := filepath.Join(resources, "agent-package.json")
	manifest, err := os.ReadFile(manifestPath)
	if os.IsNotExist(err) {
		return nil // dev-бинарь вне Brigade.app использует инструменты из PATH хоста
	}
	if err != nil {
		return err
	}
	var pkg struct {
		Dependencies map[string]string `json:"dependencies"`
	}
	if err := json.Unmarshal(manifest, &pkg); err != nil {
		return fmt.Errorf("parse %s: %w", manifestPath, err)
	}
	names := make([]string, 0, len(pkg.Dependencies))
	for name := range pkg.Dependencies {
		names = append(names, name)
	}
	sort.Strings(names)

	runtimeDir := filepath.Join(appDir, "agent-runtime")
	if err := os.MkdirAll(runtimeDir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(runtimeDir, "package.json"), manifest, 0o644); err != nil {
		return err
	}
	npmCLI := filepath.Join(resources, "node", "lib", "node_modules", "npm", "bin", "npm-cli.js")
	args := []string{npmCLI, "install", "--omit=dev", "--no-audit", "--no-fund", "--package-lock=false", "--loglevel=error", "--cache", filepath.Join(appDir, "npm-cache")}
	for _, name := range names {
		args = append(args, name+"@"+pkg.Dependencies[name])
	}
	cmd := exec.Command(filepath.Join(resources, "node", "bin", "node"), args...)
	cmd.Dir = runtimeDir
	cmd.Env = append(os.Environ(), "PATH="+filepath.Join(resources, "node", "bin")+":"+os.Getenv("PATH"))
	output, installErr := cmd.CombinedOutput()
	if installErr != nil {
		if desktopAgentRuntimeReady(runtimeDir) {
			log.Printf("brigade desktop: agent runtime update failed, keeping installed version: %v: %s", installErr, strings.TrimSpace(string(output)))
			return nil
		}
		return fmt.Errorf("npm install: %w: %s", installErr, strings.TrimSpace(string(output)))
	}
	if !desktopAgentRuntimeReady(runtimeDir) {
		return fmt.Errorf("npm install completed without required agent commands")
	}
	return nil
}

func desktopAgentRuntimeReady(runtimeDir string) bool {
	for _, name := range []string{"claude", "claude-agent-acp", "codex", "codex-acp"} {
		if _, err := os.Stat(filepath.Join(runtimeDir, "node_modules", ".bin", name)); err != nil {
			return false
		}
	}
	return true
}

// prependBundledTools ставит Node из .app и установленный per-user agent runtime перед PATH.
// No-op для dev-бинаря вне Brigade.app: там используются инструменты хоста.
func prependBundledTools(appDir, resources string) {
	if resources == "" {
		return
	}
	// Встроенный MCP-сервер brigade (render_ui/show_choice) лежит в Resources/brigade-mcp с
	// установленными зависимостями. В docker путь контейнерный (этот вызов не при чём). Без него
	// local-режим не показывал бы A2UI-карточки (напр. черновик заметки в /note).
	mcp := filepath.Join(resources, "brigade-mcp", "brigade-tools.mjs")
	if _, err := os.Stat(mcp); err == nil {
		acp.SetLocalMCPServerPath(mcp)
	}
	dirs := []string{
		filepath.Join(resources, "node", "bin"),
		filepath.Join(appDir, "agent-runtime", "node_modules", ".bin"),
	}
	for _, d := range dirs {
		if _, err := os.Stat(d); err != nil {
			return // не бандл (dev) либо рантайм не встроен — оставляем хостовый PATH
		}
	}
	_ = os.Setenv("PATH", strings.Join(dirs, ":")+":"+os.Getenv("PATH"))
}

// addrInUse сообщает, слушает ли кто-то addr (уже запущенный инстанс).
func addrInUse(addr string) bool {
	conn, err := net.DialTimeout("tcp", addr, 300*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// waitReady ждёт, пока сервер начнёт принимать соединения на addr, до таймаута.
func waitReady(addr string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if addrInUse(addr) {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}
