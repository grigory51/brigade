// Package mcp — пользовательские MCP-серверы: валидация конфига и сборка его в форму ACP
// (acpsdk.McpServer) с подстановкой секретов из vault.
//
// Секрет в конфиге не хранится: значение переменной окружения или заголовка может
// содержать ссылки "${secret.NAME}" — в любом месте строки и в любом количестве
// ("Bearer ${secret.TOKEN}", "postgres://${secret.USER}:${secret.PASS}@host/db").
// Развернуть их может только сервер — в момент, когда собирает набор для session/new
// (см. session.Registry). Дальше значение уходит по RPC в память демона сессии и в
// окружение процесса MCP-сервера, минуя диск и env контейнера.
package mcp

import (
	"fmt"
	"regexp"
	"strings"

	acpsdk "github.com/coder/acp-go-sdk"

	"github.com/grigory51/brigade/backend/internal/store"
)

// secretRef распознаёт ссылку на секрет в значении. Ссылок может быть сколько угодно и в
// любом месте строки: заголовку обычно нужен префикс ("Bearer ${secret.TOKEN}"), а строке
// подключения — несколько разных секретов сразу.
var secretRef = regexp.MustCompile(`\$\{secret\.([A-Za-z0-9_]+)\}`)

// serverName — допустимое имя сервера: оно попадает в имена инструментов модели
// (mcp__<name>__<tool>), поэтому без пробелов и спецсимволов.
var serverName = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// SecretName — допустимое имя секрета vault.
var SecretName = regexp.MustCompile(`^[A-Za-z0-9_]{1,64}$`)

// Validate проверяет конфиг сервера перед записью в store.
func Validate(srv store.McpServer) error {
	if !serverName.MatchString(srv.Name) {
		return fmt.Errorf("имя сервера: допустимы латиница, цифры, дефис и подчёркивание (до 64 символов)")
	}
	switch srv.Transport {
	case store.McpTransportStdio:
		if strings.TrimSpace(srv.Command) == "" {
			return fmt.Errorf("stdio-сервер: не задана команда")
		}
	case store.McpTransportHTTP, store.McpTransportSSE:
		if !strings.HasPrefix(srv.URL, "http://") && !strings.HasPrefix(srv.URL, "https://") {
			return fmt.Errorf("%s-сервер: URL должен начинаться с http:// или https://", srv.Transport)
		}
	default:
		return fmt.Errorf("неизвестный транспорт %q", srv.Transport)
	}
	for _, kv := range append(append([]store.McpKeyValue{}, srv.Env...), srv.Headers...) {
		if strings.TrimSpace(kv.Name) == "" {
			return fmt.Errorf("пустое имя переменной или заголовка")
		}
	}
	return nil
}

// Build собирает набор для session/new: разворачивает ссылки на секреты и переводит конфиги
// в acpsdk.McpServer. Неизвестный секрет — ошибка: молча отдать агенту сервер с пустым
// токеном значит получить непонятный отказ авторизации внутри сессии.
func Build(servers []store.McpServer, secrets map[string]string) ([]acpsdk.McpServer, error) {
	out := make([]acpsdk.McpServer, 0, len(servers))
	for _, srv := range servers {
		switch srv.Transport {
		case store.McpTransportStdio:
			env, err := resolveAll(srv, srv.Env, secrets)
			if err != nil {
				return nil, err
			}
			out = append(out, acpsdk.McpServer{Stdio: &acpsdk.McpServerStdio{
				Name:    srv.Name,
				Command: srv.Command,
				Args:    orEmpty(srv.Args),
				Env:     toEnv(env),
			}})
		case store.McpTransportHTTP:
			headers, err := resolveAll(srv, srv.Headers, secrets)
			if err != nil {
				return nil, err
			}
			out = append(out, acpsdk.McpServer{Http: &acpsdk.McpServerHttpInline{
				Name:    srv.Name,
				Url:     srv.URL,
				Headers: toHeaders(headers),
			}})
		case store.McpTransportSSE:
			headers, err := resolveAll(srv, srv.Headers, secrets)
			if err != nil {
				return nil, err
			}
			out = append(out, acpsdk.McpServer{Sse: &acpsdk.McpServerSseInline{
				Name:    srv.Name,
				Url:     srv.URL,
				Headers: toHeaders(headers),
			}})
		default:
			return nil, fmt.Errorf("mcp %q: неизвестный транспорт %q", srv.Name, srv.Transport)
		}
	}
	return out, nil
}

// resolveAll разворачивает ссылки на секреты в списке пар.
func resolveAll(srv store.McpServer, pairs []store.McpKeyValue, secrets map[string]string) ([]store.McpKeyValue, error) {
	out := make([]store.McpKeyValue, 0, len(pairs))
	for _, kv := range pairs {
		var missing string
		value := secretRef.ReplaceAllStringFunc(kv.Value, func(ref string) string {
			name := secretRef.FindStringSubmatch(ref)[1]
			v, ok := secrets[name]
			if !ok {
				missing = name
				return ref
			}
			return v
		})
		if missing != "" {
			return nil, fmt.Errorf("mcp %q: %s ссылается на секрет %q, которого нет в хранилище", srv.Name, kv.Name, missing)
		}
		out = append(out, store.McpKeyValue{Name: kv.Name, Value: value})
	}
	return out, nil
}

// SecretRefs возвращает имена секретов, на которые ссылается значение. Нужна клиенту и
// валидации: по ней видно, что в строке вообще есть ссылка.
func SecretRefs(value string) []string {
	matches := secretRef.FindAllStringSubmatch(value, -1)
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		out = append(out, m[1])
	}
	return out
}

func toEnv(pairs []store.McpKeyValue) []acpsdk.EnvVariable {
	out := make([]acpsdk.EnvVariable, 0, len(pairs))
	for _, kv := range pairs {
		out = append(out, acpsdk.EnvVariable{Name: kv.Name, Value: kv.Value})
	}
	return out
}

func toHeaders(pairs []store.McpKeyValue) []acpsdk.HttpHeader {
	out := make([]acpsdk.HttpHeader, 0, len(pairs))
	for _, kv := range pairs {
		out = append(out, acpsdk.HttpHeader{Name: kv.Name, Value: kv.Value})
	}
	return out
}

func orEmpty(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}
