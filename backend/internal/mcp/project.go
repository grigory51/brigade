package mcp

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

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

// WriteCodexConfig пишет проектный config.toml в отдельный CODEX_HOME сессии. Секреты
// остаются в переменных окружения: TOML содержит только имена env-переменных.
func WriteCodexConfig(codexHome string, servers []store.McpServer, secrets map[string]string) ([]string, error) {
	path := filepath.Join(codexHome, "config.toml")
	if len(servers) == 0 {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		return nil, nil
	}
	used := map[string]string{}
	var b strings.Builder
	for _, srv := range servers {
		name := strings.ReplaceAll(srv.Name, `"`, `\"`)
		fmt.Fprintf(&b, "[mcp_servers.\"%s\"]\n", name)
		switch srv.Transport {
		case store.McpTransportStdio:
			fmt.Fprintf(&b, "command = %s\n", strconv.Quote(srv.Command))
			b.WriteString("args = [")
			for i, arg := range srv.Args {
				if i > 0 {
					b.WriteString(", ")
				}
				b.WriteString(strconv.Quote(arg))
			}
			b.WriteString("]\n")
			env, err := placeholders(srv, srv.Env, secrets, used)
			if err != nil {
				return nil, err
			}
			if len(env) > 0 {
				b.WriteString("env = {")
				writeTOMLMap(&b, env)
				b.WriteString("}\n")
			}
		case store.McpTransportHTTP, store.McpTransportSSE:
			fmt.Fprintf(&b, "url = %s\n", strconv.Quote(srv.URL))
			literalHeaders := map[string]string{}
			envHeaders := map[string]string{}
			for _, header := range srv.Headers {
				if !secretRef.MatchString(header.Value) {
					literalHeaders[header.Name] = header.Value
					continue
				}
				var missing string
				value := secretRef.ReplaceAllStringFunc(header.Value, func(ref string) string {
					name := secretRef.FindStringSubmatch(ref)[1]
					secret, ok := secrets[name]
					if !ok {
						missing = name
						return ref
					}
					return secret
				})
				if missing != "" {
					return nil, fmt.Errorf("mcp %q: %s ссылается на секрет %q, которого нет в хранилище", srv.Name, header.Name, missing)
				}
				envName := fmt.Sprintf("BRIGADE_MCP_HEADER_%d", len(used)+1)
				used[envName] = value
				envHeaders[header.Name] = envName
			}
			if len(literalHeaders) > 0 {
				b.WriteString("http_headers = {")
				writeTOMLMap(&b, literalHeaders)
				b.WriteString("}\n")
			}
			if len(envHeaders) > 0 {
				b.WriteString("env_http_headers = {")
				writeTOMLMap(&b, envHeaders)
				b.WriteString("}\n")
			}
		default:
			return nil, fmt.Errorf("mcp %q: неизвестный транспорт %q", srv.Name, srv.Transport)
		}
		b.WriteByte('\n')
	}
	if err := os.MkdirAll(codexHome, 0o700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		return nil, err
	}
	env := make([]string, 0, len(used))
	for name, value := range used {
		env = append(env, name+"="+value)
	}
	sort.Strings(env)
	return env, nil
}

func writeTOMLMap(b *strings.Builder, values map[string]string) {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for i, key := range keys {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(b, "%s = %s", strconv.Quote(key), strconv.Quote(values[key]))
	}
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
