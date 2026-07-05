package session

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/grigory51/brigade/backend/internal/store"
)

// WorkflowInfo — состояние одного workflow-запуска харнесса агента (оркестрация
// субагентов внутри Claude Code). Собирается разбором файлов, которые харнесс пишет в
// ~/.claude/projects/<slug>/<agentSessionID>/: транскрипты и journal.jsonl появляются
// при старте, wf_<runId>.json с результатом — по завершении. Контракт файлов
// недокументирован и проверен эмпирически — при смене формата деградируем мягко
// (пустой список/неизвестное имя), не ошибкой.
type WorkflowInfo struct {
	RunID string `json:"runId"`
	Name  string `json:"name"`
	// AgentsStarted/AgentsDone — счётчики строк started/result в journal.jsonl:
	// сколько субагентов запущено и сколько вернуло результат.
	AgentsStarted int `json:"agentsStarted"`
	AgentsDone    int `json:"agentsDone"`
	// Done — воркфлоу завершён (харнесс записал wf_<runId>.json с результатом).
	Done bool `json:"done"`
	// Active — не завершён и файлы недавно менялись (транскрипты субагентов растут по
	// ходу работы). Не-Active и не-Done — брошенный/упавший запуск.
	Active bool `json:"active"`
	// LastActivitySec — секунд с последнего изменения файлов запуска.
	LastActivitySec int64 `json:"lastActivitySec"`
}

// workflowActiveWindow — окно свежести файлов, в пределах которого незавершённый
// воркфлоу считается работающим. Транскрипты субагентов пишутся часто (стрим вывода),
// поэтому окно с запасом покрывает паузы между записями.
const workflowActiveWindow = 2 * time.Minute

// workflowNameRe достаёт meta.name из текста workflow-скрипта.
var workflowNameRe = regexp.MustCompile(`name:\s*'([^']+)'`)

// nonAlnumRe — символы, заменяемые дефисом в slug проектной папки Claude Code.
var nonAlnumRe = regexp.MustCompile(`[^A-Za-z0-9]`)

// projectSlug воспроизводит имя проектной папки Claude Code для рабочей директории:
// каждый не-алфавитно-цифровой символ пути заменяется дефисом
// (/home/agent/workspace → -home-agent-workspace).
func projectSlug(cwd string) string {
	return nonAlnumRe.ReplaceAllString(cwd, "-")
}

// claudeProjectDir возвращает путь (на хосте) к проектной папке Claude Code данной
// сессии. docker: per-user home смонтирован с хоста (claudeHomeDir/<userID>);
// local: агент — процесс под пользователем brigade, его ~/.claude в home процесса.
// Пусто — определить нельзя (нет agent_session_id, выключен claudeHomeDir и т.п.).
func (r *Registry) claudeProjectDir(sess store.Session) string {
	if sess.AgentSessionID == "" {
		return ""
	}
	var home string
	switch sess.Mode {
	case store.SessionModeDocker:
		if r.claudeHomeDir == "" {
			return ""
		}
		home = filepath.Join(r.claudeHomeDir, sess.UserID)
	default:
		h, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		home = h
	}
	return filepath.Join(home, ".claude", "projects", projectSlug(sess.Cwd), sess.AgentSessionID)
}

// Workflows возвращает workflow-запуски харнесса агента сессии (бегущие и недавние).
// ok=false — сессия неизвестна, не ACP или принадлежит другому пользователю.
func (r *Registry) Workflows(ctx context.Context, sessionID, userID string) ([]WorkflowInfo, bool) {
	sess, err := r.store.GetSession(ctx, sessionID)
	if err != nil || sess.UserID != userID || sess.Kind != store.SessionKindACP {
		return nil, false
	}

	base := r.claudeProjectDir(sess)
	if base == "" {
		return []WorkflowInfo{}, true
	}

	runsDir := filepath.Join(base, "subagents", "workflows")
	entries, err := os.ReadDir(runsDir)
	if err != nil {
		// Каталог появляется с первым workflow-запуском — отсутствие означает
		// «воркфлоу не запускались», это не ошибка.
		return []WorkflowInfo{}, true
	}

	var out []WorkflowInfo
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), "wf_") {
			continue
		}
		out = append(out, r.workflowInfo(base, e.Name()))
	}
	// Сначала бегущие, затем по свежести: панель показывает актуальное сверху.
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			less := func(a, b WorkflowInfo) bool {
				if a.Active != b.Active {
					return a.Active
				}
				return a.LastActivitySec < b.LastActivitySec
			}
			if less(out[j], out[i]) {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	if len(out) > 10 {
		out = out[:10]
	}
	if out == nil {
		out = []WorkflowInfo{}
	}
	return out, true
}

// workflowInfo собирает состояние одного запуска по его файлам.
func (r *Registry) workflowInfo(base, runID string) WorkflowInfo {
	info := WorkflowInfo{RunID: runID, Name: runID}
	runDir := filepath.Join(base, "subagents", "workflows", runID)

	// Прогресс — по journal.jsonl: started/result на каждого субагента.
	if f, err := os.Open(filepath.Join(runDir, "journal.jsonl")); err == nil {
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024) // result-строки бывают крупными
		for sc.Scan() {
			var line struct {
				Type string `json:"type"`
			}
			if json.Unmarshal(sc.Bytes(), &line) != nil {
				continue
			}
			switch line.Type {
			case "started":
				info.AgentsStarted++
			case "result":
				info.AgentsDone++
			}
		}
		_ = f.Close()
	}

	// Последняя активность — максимальный mtime файлов запуска (транскрипты растут).
	var last time.Time
	if entries, err := os.ReadDir(runDir); err == nil {
		for _, e := range entries {
			if fi, err := e.Info(); err == nil && fi.ModTime().After(last) {
				last = fi.ModTime()
			}
		}
	}
	if !last.IsZero() {
		info.LastActivitySec = int64(time.Since(last).Seconds())
	}

	// Завершённость — по wf_<runId>.json; из него же имя (meta.name в тексте скрипта).
	metaPath := filepath.Join(base, "workflows", runID+".json")
	if raw, err := os.ReadFile(metaPath); err == nil {
		info.Done = true
		var meta struct {
			Script string `json:"script"`
		}
		if json.Unmarshal(raw, &meta) == nil {
			if m := workflowNameRe.FindStringSubmatch(meta.Script); m != nil {
				info.Name = m[1]
			}
		}
	} else {
		// Бегущий/брошенный: имя из файла скрипта <name>-<runId>.js.
		pattern := filepath.Join(base, "workflows", "scripts", fmt.Sprintf("*-%s.js", runID))
		if matches, _ := filepath.Glob(pattern); len(matches) > 0 {
			name := strings.TrimSuffix(filepath.Base(matches[0]), fmt.Sprintf("-%s.js", runID))
			if name != "" {
				info.Name = name
			}
		}
	}

	info.Active = !info.Done && !last.IsZero() && time.Since(last) < workflowActiveWindow
	return info
}
