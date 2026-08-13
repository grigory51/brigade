// Package agent ведёт реестр доступных типов агентов и параметры их запуска.
//
// Манифест — единственный источник истины о том, ЧТО brigade привозит в среду агента и
// ЧЕМ его запускает. Состав зависит от пары (агент, вид сессии): CLI-сессии Claude нужен
// только сам `claude`, ACP-сессии — ACP-адаптер и MCP-сервер brigade. Раньше это было
// размазано по трём хардкодам (путь бинаря демона в spawn, имя команды CLI в реестре
// сессий, путь MCP-сервера в acp), из-за чего добавление второго агента требовало правок
// в каждом из них.
//
// Компоненты приезжают в контейнер отдельными read-only volume'ами (см. spawn), а не
// берутся из образа: это позволяет пользователю принести СВОЙ образ (ubuntu, golang, …) и
// не даёт подменить бинарь демона, которому brigade передаёт секреты.
package agent

import "github.com/grigory51/brigade/backend/internal/store"

// RuntimeRoot — корень runtime brigade внутри контейнера; каждый слой монтируется в свой
// подкаталог RuntimeRoot/<layer>.
const RuntimeRoot = "/opt/brigade-runtime"

// Layer — компонент runtime: каталог в образе-доноре, он же отдельный named volume.
// Bin — подкаталог с исполняемыми файлами, попадающий в PATH контейнера (пусто — слой
// исполняемых файлов не несёт).
type Layer struct {
	Name string
	Bin  string
}

// Path — путь слоя внутри контейнера.
func (l Layer) Path() string { return RuntimeRoot + "/" + l.Name }

// BinPath — каталог исполняемых файлов слоя (пусто, если их нет).
func (l Layer) BinPath() string {
	if l.Bin == "" {
		return ""
	}
	return l.Path() + "/" + l.Bin
}

// Слои runtime. Имя слоя совпадает с каталогом в образе-доноре (см.
// packaging/docker/agent/Dockerfile) — расхождение ловится при подготовке volume'ов.
var (
	// LayerDaemon — бинарь brigade (`brigade acp-agent`, pid1 контейнера). Приезжает
	// всегда и ко всем агентам: именно ему brigade передаёт по RPC секреты сессии, и он
	// обязан быть нашим независимо от содержимого образа.
	LayerDaemon = Layer{Name: "daemon", Bin: "bin"}
	// LayerNode — Node.js для агентов, которые на нём написаны.
	LayerNode = Layer{Name: "node", Bin: "bin"}
	// LayerClaudeCLI — Claude Code CLI (интерактивный режим, `claude` в терминале).
	LayerClaudeCLI = Layer{Name: "claude-cli", Bin: "bin"}
	// LayerACPAdapter — ACP-адаптер поверх Claude Agent SDK (структурированный режим).
	LayerACPAdapter = Layer{Name: "acp-adapter", Bin: "bin"}
	// LayerCodex — официальный Codex CLI и его ACP-адаптер в одном runtime-слое.
	LayerCodex = Layer{Name: "codex", Bin: "bin"}
	// LayerBrigadeMCP — stdio MCP-сервер brigade с кастомными UI-инструментами
	// (render_ui, show_choice). Нужен только ACP: в CLI-режиме их некому отрисовать.
	LayerBrigadeMCP = Layer{Name: "brigade-mcp"}
)

// Type — тип агента: как он показывается пользователю и что нужно для его запуска.
type Type struct {
	ID   string
	Name string
	// Layers — компоненты runtime по видам сессии (сверх LayerDaemon, который приезжает
	// всегда).
	Layers map[store.SessionKind][]Layer
	// Command — исполняемый файл агента по видам сессии: для CLI это команда терминала,
	// для ACP — команда адаптера (acpremote.ConfigureOptions.AdapterCommand).
	Command map[store.SessionKind]string
	// McpServerScript — путь к скрипту MCP-сервера brigade внутри runtime. Пусто — агент
	// его не получает.
	McpServerScript string
	// RotatingCredentials описывает файловые credential, которые агент может менять сам.
	// Ключ — auth_profile подключения; BaseEnv задаёт каталог, RelativePath — файл в нём.
	RotatingCredentials map[string]CredentialFile
}

type CredentialFile struct {
	BaseEnv      string
	RelativePath string
}

// Claude — Claude Code: один агент на оба вида сессии, но с разными компонентами.
var Claude = Type{
	ID:   "claude-code",
	Name: "Claude Code",
	Layers: map[store.SessionKind][]Layer{
		store.SessionKindCLI: {LayerNode, LayerClaudeCLI},
		store.SessionKindACP: {LayerNode, LayerACPAdapter, LayerBrigadeMCP},
	},
	Command: map[store.SessionKind]string{
		store.SessionKindCLI: "claude",
		store.SessionKindACP: "claude-agent-acp",
	},
	McpServerScript: LayerBrigadeMCP.Path() + "/brigade-tools.mjs",
}

// Codex — официальный Codex CLI через ACP-адаптер codex-acp либо интерактивный CLI.
var Codex = Type{
	ID:   "codex",
	Name: "Codex",
	Layers: map[store.SessionKind][]Layer{
		store.SessionKindCLI: {LayerNode, LayerCodex},
		store.SessionKindACP: {LayerNode, LayerCodex, LayerBrigadeMCP},
	},
	Command: map[store.SessionKind]string{
		store.SessionKindCLI: "codex",
		store.SessionKindACP: "codex-acp",
	},
	McpServerScript: LayerBrigadeMCP.Path() + "/brigade-tools.mjs",
	RotatingCredentials: map[string]CredentialFile{
		"chatgpt": {BaseEnv: "CODEX_HOME", RelativePath: "auth.json"},
	},
}

// types — реестр доступных агентов. Пока один; форма манифеста рассчитана на то, что
// второй добавляется записью здесь, без правок в spawn и реестре сессий.
var types = []Type{Claude, Codex}

// List возвращает доступные типы агентов.
func List() []Type { return types }

// Get возвращает тип агента по идентификатору. Неизвестный id — дефолтный агент: сессии
// прежних версий и клиенты, не присылающие agent_type, должны продолжать работать.
func Get(id string) Type {
	for _, t := range types {
		if t.ID == id {
			return t
		}
	}
	return types[0]
}

// LayersFor — компоненты runtime для сессии: слой демона плюс объявленные агентом для
// этого вида сессии.
func (t Type) LayersFor(kind store.SessionKind) []Layer {
	return append([]Layer{LayerDaemon}, t.Layers[kind]...)
}

// CommandFor — исполняемый файл агента для вида сессии (пусто, если вид не поддержан).
func (t Type) CommandFor(kind store.SessionKind) string { return t.Command[kind] }
