package mcp

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/grigory51/brigade/backend/internal/store"
)

// Проектный конфиг MCP для CLI-агента: `claude` в терминале читает набор серверов из
// .mcp.json рабочей директории, а не получает его по протоколу, как ACP-адаптер. Поэтому
// для CLI-сессии brigade раскладывает файл рядом с агентом.
//
// Секреты в файл не попадают: вместо значения пишется ссылка на переменную окружения
// (${BRIGADE_SECRET_ИМЯ}), которую Claude Code раскрывает при старте сервера. Сами значения
// уходят в окружение процесса `claude` (см. registry.agentEnv) — не в env контейнера и не
// на диск.
const (
	projectConfigRel = ".mcp.json"
	settingsRel      = ".claude/settings.json"
	// envPrefix — префикс переменных окружения, в которые кладутся значения секретов.
	envPrefix = "BRIGADE_SECRET_"
)

// WriteProjectConfig раскладывает .mcp.json в cwd и возвращает переменные окружения
// ("ИМЯ=значение") со значениями секретов, на которые ссылается конфиг. Пустой набор
// серверов удаляет файл: выключение сервера должно убирать его из сессии, а не оставлять
// прошлый конфиг. Неизвестный секрет — ошибка, как и при сборке набора для ACP.
func WriteProjectConfig(cwd string, servers []store.McpServer, secrets map[string]string) ([]string, error) {
	path := filepath.Join(cwd, projectConfigRel)
	if len(servers) == 0 {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("mcp: удаление %s: %w", path, err)
		}
		return nil, nil
	}

	used := map[string]string{} // имя переменной окружения → значение секрета
	entries := map[string]any{}
	for _, srv := range servers {
		switch srv.Transport {
		case store.McpTransportStdio:
			env, err := placeholders(srv, srv.Env, secrets, used)
			if err != nil {
				return nil, err
			}
			entries[srv.Name] = map[string]any{
				"type":    "stdio",
				"command": srv.Command,
				"args":    orEmpty(srv.Args),
				"env":     env,
			}
		case store.McpTransportHTTP, store.McpTransportSSE:
			headers, err := placeholders(srv, srv.Headers, secrets, used)
			if err != nil {
				return nil, err
			}
			entries[srv.Name] = map[string]any{
				"type":    string(srv.Transport),
				"url":     srv.URL,
				"headers": headers,
			}
		default:
			return nil, fmt.Errorf("mcp %q: неизвестный транспорт %q", srv.Name, srv.Transport)
		}
	}

	data, err := json.MarshalIndent(map[string]any{"mcpServers": entries}, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("mcp: сериализация %s: %w", projectConfigRel, err)
	}
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		return nil, fmt.Errorf("mcp: mkdir %s: %w", cwd, err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return nil, fmt.Errorf("mcp: запись %s: %w", path, err)
	}
	if err := approveProjectServers(filepath.Join(cwd, filepath.FromSlash(settingsRel))); err != nil {
		return nil, err
	}

	env := make([]string, 0, len(used))
	for name, value := range used {
		env = append(env, name+"="+value)
	}
	return env, nil
}

// placeholders переводит пары в map для .mcp.json, заменяя ссылки на секреты ссылками на
// переменные окружения и запоминая нужные значения в used.
func placeholders(srv store.McpServer, pairs []store.McpKeyValue, secrets map[string]string, used map[string]string) (map[string]string, error) {
	out := make(map[string]string, len(pairs))
	for _, kv := range pairs {
		var missing string
		value := secretRef.ReplaceAllStringFunc(kv.Value, func(ref string) string {
			name := secretRef.FindStringSubmatch(ref)[1]
			v, ok := secrets[name]
			if !ok {
				missing = name
				return ref
			}
			used[envPrefix+name] = v
			return "${" + envPrefix + name + "}"
		})
		if missing != "" {
			return nil, fmt.Errorf("mcp %q: %s ссылается на секрет %q, которого нет в хранилище", srv.Name, kv.Name, missing)
		}
		out[kv.Name] = value
	}
	return out, nil
}

// approveProjectServers включает серверы проекта в .claude/settings.json. Без этого Claude
// Code при первом запуске спрашивает подтверждение на каждый сервер из .mcp.json, а
// неинтерактивного ответа у brigade нет. Прочие ключи файла сохраняются: рядом лежат
// настройки плагина brigade (см. preview.InstallSkill).
func approveProjectServers(path string) error {
	settings := map[string]any{}
	if data, err := os.ReadFile(path); err == nil {
		// Повреждённый файл перезапишем валидным; иначе сохраняем существующие ключи.
		_ = json.Unmarshal(data, &settings)
	}
	settings["enableAllProjectMcpServers"] = true

	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("mcp: сериализация %s: %w", settingsRel, err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mcp: mkdir %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("mcp: запись %s: %w", path, err)
	}
	return nil
}
