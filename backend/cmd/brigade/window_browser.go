//go:build !(darwin && cgo)

package main

import (
	"log"
	"os/exec"
	"runtime"
)

// showWindow — fallback без нативного webview (не-macOS либо сборка без cgo): открывает url в
// системном браузере и блокируется (сервер продолжает работать в фоне; выход — по завершению
// процесса). Нативное окно доступно только в mac-сборке с cgo (см. window_webview.go).
func showWindow(url, _ string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		log.Printf("brigade desktop: не удалось открыть браузер: %v; откройте вручную: %s", err, url)
	} else {
		log.Printf("brigade desktop: интерфейс открыт в браузере: %s", url)
	}
	select {} // держим процесс живым: сервер работает в фоне
}
