package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
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

// runDesktop — точка входа подкоманды desktop.
func runDesktop() {
	appDir, err := desktopAppDir()
	if err != nil {
		log.Fatalf("brigade desktop: app dir: %v", err)
	}
	cfgPath := filepath.Join(appDir, "config.yaml")
	if err := ensureDesktopConfig(appDir, cfgPath); err != nil {
		log.Fatalf("brigade desktop: config: %v", err)
	}
	enrichPATH()
	prependBundledTools()

	// Стартуем с /desktop/auth: ручка ставит сессионные cookie сид-пользователя и редиректит на
	// SPA — приложение открывается сразу залогиненным, без экрана входа (локальный однопользоват.).
	url := "http://" + desktopAddr + "/desktop/auth"
	desktopMode = true
	// Если порт уже слушают (уже запущенный инстанс) — второй сервер не поднимаем, просто
	// открываем окно к нему. Иначе стартуем сервер в фоне и ждём готовности.
	if !addrInUse(desktopAddr) {
		go runServer(cfgPath)
		if !waitReady(desktopAddr, 15*time.Second) {
			log.Fatalf("brigade desktop: сервер не поднялся за отведённое время")
		}
	}
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
# Чтобы переключить режим на изоляцию в контейнерах — mode: docker (нужен запущенный Docker
# Desktop) и перезапустите приложение.
mode: local
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

// prependBundledTools ставит встроенные в .app каталоги агент-рантайма (node + claude-agent-acp
// + claude) ПЕРЕД остальным PATH, чтобы local-режим использовал самодостаточные версии из
// бандла, а не глобальный npm хоста (тот дрейфует по версиям). No-op, если бинарь запущен не из
// .app (dev-режим) — тогда используется хостовый PATH.
func prependBundledTools() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	// exe: <Brigade.app>/Contents/MacOS/brigade-bin → Resources рядом с MacOS.
	res := filepath.Join(filepath.Dir(exe), "..", "Resources")
	dirs := []string{
		filepath.Join(res, "node", "bin"),
		filepath.Join(res, "agent", "node_modules", ".bin"),
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
