package acp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	acpsdk "github.com/coder/acp-go-sdk"

	"github.com/grigory51/brigade/backend/internal/agui"
)

// Здесь реализованы методы интерфейса acp.Client — callbacks, которые агент вызывает
// по ходу turn'а. Это server-side по отношению к brigade: агент инициирует, мы отвечаем.

// SessionUpdate принимает потоковое обновление сессии от агента, транслирует его в
// AG-UI и доставляет клиенту через sink. Ошибки sink (мёртвый WS) проглатываются:
// прерывать turn агента из-за отвалившегося клиента нельзя — состояние сессии должно
// дойти до конца и сохраниться для resume.
func (c *Client) SessionUpdate(ctx context.Context, params acpsdk.SessionNotification) error {
	// translateUpdate читает и обновляет c.stream под c.mu; emit берёт c.mu сам,
	// поэтому транслируем под мьютексом, а доставляем уже без него.
	c.mu.Lock()
	evts := c.translateUpdate(params.Update)
	c.mu.Unlock()
	for _, evt := range evts {
		c.emit(evt)
	}
	return nil
}

// RequestPermission запрашивает у пользователя разрешение на действие агента и
// блокируется до ответа. Запрос отдаётся клиенту через resolver (PERMISSION_REQUEST по
// WS); resolver возвращает выбранный OptionID, который мы возвращаем агенту как исход
// selected. Если resolver не задан или вернул ошибку (клиент отключился, ctx отменён),
// исход трактуется как cancelled согласно ACP-контракту.
func (c *Client) RequestPermission(ctx context.Context, params acpsdk.RequestPermissionRequest) (acpsdk.RequestPermissionResponse, error) {
	c.mu.Lock()
	resolver := c.resolver
	c.mu.Unlock()

	// Вне WS-сеанса (resolver не привязан) разрешить интерактивное действие некому —
	// отвечаем агенту cancelled, чтобы он корректно свернул turn.
	if resolver == nil {
		return acpsdk.RequestPermissionResponse{
			Outcome: acpsdk.RequestPermissionOutcome{Cancelled: &acpsdk.RequestPermissionOutcomeCancelled{}},
		}, nil
	}

	req := agui.PermissionRequest{
		ID:         string(params.ToolCall.ToolCallId),
		ToolCallID: string(params.ToolCall.ToolCallId),
		Options:    make([]agui.PermissionOption, 0, len(params.Options)),
	}
	if params.ToolCall.Title != nil {
		req.Title = *params.ToolCall.Title
	}
	for _, opt := range params.Options {
		req.Options = append(req.Options, agui.PermissionOption{
			OptionID: string(opt.OptionId),
			Name:     opt.Name,
			Kind:     string(opt.Kind),
		})
	}

	optionID, err := resolver(ctx, req)
	if err != nil {
		// Отмена (клиент закрыл WS / ctx отменён) — отвечаем агенту cancelled, чтобы он
		// корректно свернул turn.
		return acpsdk.RequestPermissionResponse{
			Outcome: acpsdk.RequestPermissionOutcome{Cancelled: &acpsdk.RequestPermissionOutcomeCancelled{}},
		}, nil
	}
	return acpsdk.RequestPermissionResponse{
		Outcome: acpsdk.RequestPermissionOutcome{
			Selected: &acpsdk.RequestPermissionOutcomeSelected{
				OptionId: acpsdk.PermissionOptionId(optionID),
			},
		},
	}, nil
}

// ReadTextFile обслуживает запрос агента на чтение файла. Путь обязан быть абсолютным
// (требование ACP). Поддерживается частичное чтение по line/limit (1-based).
func (c *Client) ReadTextFile(ctx context.Context, params acpsdk.ReadTextFileRequest) (acpsdk.ReadTextFileResponse, error) {
	if !filepath.IsAbs(params.Path) {
		return acpsdk.ReadTextFileResponse{}, fmt.Errorf("acp: путь должен быть абсолютным: %s", params.Path)
	}
	b, err := os.ReadFile(params.Path)
	if err != nil {
		return acpsdk.ReadTextFileResponse{}, fmt.Errorf("acp: чтение %s: %w", params.Path, err)
	}
	content := string(b)
	if params.Line != nil || params.Limit != nil {
		content = sliceLines(content, params.Line, params.Limit)
	}
	return acpsdk.ReadTextFileResponse{Content: content}, nil
}

// WriteTextFile обслуживает запрос агента на запись файла, создавая недостающие каталоги.
func (c *Client) WriteTextFile(ctx context.Context, params acpsdk.WriteTextFileRequest) (acpsdk.WriteTextFileResponse, error) {
	if !filepath.IsAbs(params.Path) {
		return acpsdk.WriteTextFileResponse{}, fmt.Errorf("acp: путь должен быть абсолютным: %s", params.Path)
	}
	if dir := filepath.Dir(params.Path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return acpsdk.WriteTextFileResponse{}, fmt.Errorf("acp: mkdir %s: %w", dir, err)
		}
	}
	if err := os.WriteFile(params.Path, []byte(params.Content), 0o644); err != nil {
		return acpsdk.WriteTextFileResponse{}, fmt.Errorf("acp: запись %s: %w", params.Path, err)
	}
	return acpsdk.WriteTextFileResponse{}, nil
}

// sliceLines возвращает срез content по 1-based номеру строки и лимиту строк.
func sliceLines(content string, line, limit *int) string {
	lines := strings.Split(content, "\n")
	start := 0
	if line != nil && *line > 0 {
		start = min(max(*line-1, 0), len(lines))
	}
	end := len(lines)
	if limit != nil && *limit > 0 && start+*limit < end {
		end = start + *limit
	}
	return strings.Join(lines[start:end], "\n")
}

// Терминальные методы ACP в brigade не используются (CLI-режим живёт в отдельном
// транспорте termws через pty, не через ACP-терминалы). Реализованы как no-op, чтобы
// удовлетворить интерфейс acp.Client; возвращают пустые ответы.

func (c *Client) CreateTerminal(ctx context.Context, params acpsdk.CreateTerminalRequest) (acpsdk.CreateTerminalResponse, error) {
	return acpsdk.CreateTerminalResponse{}, fmt.Errorf("acp: терминалы не поддерживаются")
}

func (c *Client) KillTerminal(ctx context.Context, params acpsdk.KillTerminalRequest) (acpsdk.KillTerminalResponse, error) {
	return acpsdk.KillTerminalResponse{}, nil
}

func (c *Client) TerminalOutput(ctx context.Context, params acpsdk.TerminalOutputRequest) (acpsdk.TerminalOutputResponse, error) {
	return acpsdk.TerminalOutputResponse{}, fmt.Errorf("acp: терминалы не поддерживаются")
}

func (c *Client) ReleaseTerminal(ctx context.Context, params acpsdk.ReleaseTerminalRequest) (acpsdk.ReleaseTerminalResponse, error) {
	return acpsdk.ReleaseTerminalResponse{}, nil
}

func (c *Client) WaitForTerminalExit(ctx context.Context, params acpsdk.WaitForTerminalExitRequest) (acpsdk.WaitForTerminalExitResponse, error) {
	return acpsdk.WaitForTerminalExitResponse{}, fmt.Errorf("acp: терминалы не поддерживаются")
}
