package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"
)

const telegramAPI = "https://api.telegram.org"

type botAPI struct {
	http    *http.Client
	baseURL string
}

func newBotAPI() *botAPI {
	return &botAPI{http: &http.Client{Timeout: 40 * time.Second}, baseURL: telegramAPI}
}

type telegramUser struct {
	ID                   int64  `json:"id"`
	IsBot                bool   `json:"is_bot"`
	FirstName            string `json:"first_name"`
	Username             string `json:"username"`
	SupportsGuestQueries bool   `json:"supports_guest_queries"`
	HasTopicsEnabled     bool   `json:"has_topics_enabled"`
}

type telegramChat struct {
	ID       int64  `json:"id"`
	Type     string `json:"type"`
	Title    string `json:"title"`
	Username string `json:"username"`
}

type telegramMessage struct {
	MessageID          int64              `json:"message_id"`
	MessageThreadID    int64              `json:"message_thread_id"`
	MediaGroupID       string             `json:"media_group_id"`
	GuestQueryID       string             `json:"guest_query_id"`
	From               *telegramUser      `json:"from"`
	GuestBotCallerUser *telegramUser      `json:"guest_bot_caller_user"`
	Chat               telegramChat       `json:"chat"`
	Text               string             `json:"text"`
	Caption            string             `json:"caption"`
	Animation          *telegramFile      `json:"animation"`
	Audio              *telegramFile      `json:"audio"`
	Document           *telegramFile      `json:"document"`
	LivePhoto          *telegramLivePhoto `json:"live_photo"`
	Photo              []telegramFile     `json:"photo"`
	Sticker            *telegramFile      `json:"sticker"`
	Video              *telegramFile      `json:"video"`
	VideoNote          *telegramFile      `json:"video_note"`
	Voice              *telegramFile      `json:"voice"`
	RichMessage        json.RawMessage    `json:"rich_message"`
	PaidMedia          json.RawMessage    `json:"paid_media"`
	Checklist          json.RawMessage    `json:"checklist"`
	Contact            json.RawMessage    `json:"contact"`
	Dice               json.RawMessage    `json:"dice"`
	Game               json.RawMessage    `json:"game"`
	Poll               json.RawMessage    `json:"poll"`
	Venue              json.RawMessage    `json:"venue"`
	Location           json.RawMessage    `json:"location"`
	Story              json.RawMessage    `json:"story"`
	WebAppData         json.RawMessage    `json:"web_app_data"`
	ReplyToMessage     *telegramMessage   `json:"reply_to_message"`
}

type telegramFile struct {
	FileID       string `json:"file_id"`
	FileUniqueID string `json:"file_unique_id"`
	FileName     string `json:"file_name"`
	MimeType     string `json:"mime_type"`
	FileSize     int64  `json:"file_size"`
	Width        int64  `json:"width"`
	Height       int64  `json:"height"`
	Emoji        string `json:"emoji"`
}

type telegramLivePhoto struct {
	telegramFile
	Photo []telegramFile `json:"photo"`
}

type telegramRemoteFile struct {
	FileID   string `json:"file_id"`
	FileSize int64  `json:"file_size"`
	FilePath string `json:"file_path"`
}

type telegramUpdate struct {
	UpdateID             int64            `json:"update_id"`
	Message              *telegramMessage `json:"message"`
	GuestMessage         *telegramMessage `json:"guest_message"`
	GuestInlineMessageID string           `json:"brigade_guest_inline_message_id,omitempty"`
}

func (a *botAPI) call(ctx context.Context, token, method string, in, out any) error {
	body, err := json.Marshal(in)
	if err != nil {
		return fmt.Errorf("telegram: encode %s: %w", method, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.baseURL+"/bot"+token+"/"+method, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("telegram: request %s: invalid Bot API URL", method)
	}
	req.Header.Set("Content-Type", "application/json")
	return a.do(req, method, out)
}

func (a *botAPI) do(req *http.Request, method string, out any) error {
	resp, err := a.http.Do(req)
	if err != nil {
		// url.Error содержит полный URL, а Bot API включает секретный token в path.
		for {
			var urlErr *url.Error
			if !errors.As(err, &urlErr) {
				break
			}
			err = urlErr.Err
		}
		return fmt.Errorf("telegram: %s: %w", method, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return fmt.Errorf("telegram: read %s: %w", method, err)
	}
	var envelope struct {
		OK          bool            `json:"ok"`
		Description string          `json:"description"`
		Result      json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return fmt.Errorf("telegram: decode %s: %w", method, err)
	}
	if !envelope.OK {
		return fmt.Errorf("telegram: %s: %s", method, envelope.Description)
	}
	if out != nil && len(envelope.Result) > 0 {
		if err := json.Unmarshal(envelope.Result, out); err != nil {
			return fmt.Errorf("telegram: decode %s result: %w", method, err)
		}
	}
	return nil
}

func (a *botAPI) callMultipart(ctx context.Context, token, method string, fields map[string]string, filename string, data []byte, out any) error {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for name, value := range fields {
		if err := writer.WriteField(name, value); err != nil {
			return fmt.Errorf("telegram: encode %s: %w", method, err)
		}
	}
	part, err := writer.CreateFormFile("photo", filename)
	if err != nil {
		return fmt.Errorf("telegram: encode %s: %w", method, err)
	}
	if _, err := part.Write(data); err != nil {
		return fmt.Errorf("telegram: encode %s: %w", method, err)
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("telegram: encode %s: %w", method, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.baseURL+"/bot"+token+"/"+method, &body)
	if err != nil {
		return fmt.Errorf("telegram: request %s: invalid Bot API URL", method)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	return a.do(req, method, out)
}

func (a *botAPI) sendPhoto(ctx context.Context, token string, chatID, threadID int64, filename string, data []byte, silent bool) (telegramMessage, error) {
	fields := map[string]string{"chat_id": strconv.FormatInt(chatID, 10)}
	if threadID != 0 {
		fields["message_thread_id"] = strconv.FormatInt(threadID, 10)
	}
	if silent {
		fields["disable_notification"] = "true"
	}
	var message telegramMessage
	err := a.callMultipart(ctx, token, "sendPhoto", fields, filename, data, &message)
	return message, err
}

func (a *botAPI) deleteMessage(ctx context.Context, token string, chatID, messageID int64) error {
	return a.call(ctx, token, "deleteMessage", map[string]any{"chat_id": chatID, "message_id": messageID}, nil)
}

func (a *botAPI) getMe(ctx context.Context, token string) (telegramUser, error) {
	var user telegramUser
	err := a.call(ctx, token, "getMe", struct{}{}, &user)
	return user, err
}

func (a *botAPI) downloadFile(ctx context.Context, token, fileID string, maxBytes int64) ([]byte, string, error) {
	var file telegramRemoteFile
	if err := a.call(ctx, token, "getFile", map[string]any{"file_id": fileID}, &file); err != nil {
		return nil, "", err
	}
	if file.FilePath == "" {
		return nil, "", errors.New("telegram: getFile returned no file path")
	}
	if file.FileSize > maxBytes {
		return nil, "", fmt.Errorf("telegram: file exceeds %d MiB", maxBytes>>20)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.baseURL+"/file/bot"+token+"/"+strings.TrimLeft(file.FilePath, "/"), nil)
	if err != nil {
		return nil, "", errors.New("telegram: invalid file URL")
	}
	resp, err := a.http.Do(req)
	if err != nil {
		for {
			var urlErr *url.Error
			if !errors.As(err, &urlErr) {
				break
			}
			err = urlErr.Err
		}
		return nil, "", fmt.Errorf("telegram: download file: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", fmt.Errorf("telegram: download file: HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return nil, "", fmt.Errorf("telegram: download file: %w", err)
	}
	if int64(len(data)) > maxBytes {
		return nil, "", fmt.Errorf("telegram: file exceeds %d MiB", maxBytes>>20)
	}
	return data, path.Base(file.FilePath), nil
}

func (a *botAPI) getUpdates(ctx context.Context, token string, offset int64) ([]telegramUpdate, error) {
	var updates []telegramUpdate
	err := a.call(ctx, token, "getUpdates", map[string]any{
		"offset": offset, "timeout": 30,
		"allowed_updates": []string{"message", "guest_message"},
	}, &updates)
	return updates, err
}

func (a *botAPI) setWebhook(ctx context.Context, token, webhookURL, secret string) error {
	return a.call(ctx, token, "setWebhook", map[string]any{
		"url": webhookURL, "secret_token": secret,
		"max_connections": 1,
		"allowed_updates": []string{"message", "guest_message"},
	}, nil)
}

func (a *botAPI) deleteWebhook(ctx context.Context, token string) error {
	return a.call(ctx, token, "deleteWebhook", map[string]any{"drop_pending_updates": false}, nil)
}

func (a *botAPI) sendMessage(ctx context.Context, token string, chatID, threadID int64, text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		text = "Агент не вернул текстовый ответ."
	}
	if len([]rune(text)) <= 32768 {
		in := map[string]any{
			"chat_id":      chatID,
			"rich_message": map[string]any{"markdown": text},
		}
		if threadID != 0 {
			in["message_thread_id"] = threadID
		}
		if err := a.call(ctx, token, "sendRichMessage", in, nil); err == nil {
			return nil
		}
	}
	chunks := splitMessage(text, 4096)
	for _, chunk := range chunks {
		in := map[string]any{"chat_id": chatID, "text": chunk}
		if threadID != 0 {
			in["message_thread_id"] = threadID
		}
		if err := a.call(ctx, token, "sendMessage", in, nil); err != nil {
			return err
		}
	}
	return nil
}

func (a *botAPI) sendTyping(ctx context.Context, token string, chatID, threadID int64) error {
	in := map[string]any{"chat_id": chatID, "action": "typing"}
	if threadID != 0 {
		in["message_thread_id"] = threadID
	}
	return a.call(ctx, token, "sendChatAction", in, nil)
}

func (a *botAPI) setReaction(ctx context.Context, token string, chatID, messageID int64, emoji string) error {
	reaction := []any{}
	if emoji != "" {
		reaction = append(reaction, map[string]any{"type": "emoji", "emoji": emoji})
	}
	return a.call(ctx, token, "setMessageReaction", map[string]any{
		"chat_id": chatID, "message_id": messageID, "reaction": reaction,
	}, nil)
}

func (a *botAPI) answerGuest(ctx context.Context, token, queryID, text string) (string, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		text = "Агент не вернул текстовый ответ."
	}
	if len([]rune(text)) > 32768 {
		text = string([]rune(text)[:32752]) + "\n\n…"
	}
	rich := map[string]any{
		"guest_query_id": queryID,
		"result": map[string]any{
			"type": "article", "id": "brigade", "title": "Brigade",
			"input_message_content": map[string]any{
				"rich_message": map[string]any{"markdown": text},
			},
		},
	}
	var sent struct {
		InlineMessageID string `json:"inline_message_id"`
	}
	if err := a.call(ctx, token, "answerGuestQuery", rich, &sent); err == nil {
		if sent.InlineMessageID == "" {
			return "", errors.New("telegram: answerGuestQuery returned no inline message id")
		}
		return sent.InlineMessageID, nil
	}
	plainText := text
	if len([]rune(plainText)) > 4096 {
		plainText = string([]rune(plainText)[:4080]) + "\n\n…"
	}
	plain := map[string]any{
		"guest_query_id": queryID,
		"result": map[string]any{
			"type": "article", "id": "brigade", "title": "Brigade",
			"input_message_content": map[string]any{"message_text": plainText},
		},
	}
	if err := a.call(ctx, token, "answerGuestQuery", plain, &sent); err != nil {
		return "", err
	}
	if sent.InlineMessageID == "" {
		return "", errors.New("telegram: answerGuestQuery returned no inline message id")
	}
	return sent.InlineMessageID, nil
}

func (a *botAPI) editGuest(ctx context.Context, token, inlineMessageID, text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		text = "Агент не вернул текстовый ответ."
	}
	if len([]rune(text)) > 32768 {
		text = string([]rune(text)[:32752]) + "\n\n…"
	}
	if err := a.call(ctx, token, "editMessageText", map[string]any{
		"inline_message_id": inlineMessageID,
		"rich_message":      map[string]any{"markdown": text},
	}, nil); err == nil {
		return nil
	}
	if len([]rune(text)) > 4096 {
		text = string([]rune(text)[:4080]) + "\n\n…"
	}
	return a.call(ctx, token, "editMessageText", map[string]any{
		"inline_message_id": inlineMessageID,
		"text":              text,
	}, nil)
}

func (a *botAPI) editGuestImages(ctx context.Context, token, inlineMessageID, text string, fileIDs []string) error {
	text = strings.TrimSpace(text)
	if len([]rune(text)) > 32000 {
		text = string([]rune(text)[:32000]) + "\n\n…"
	}
	media := make([]map[string]any, 0, len(fileIDs))
	for index, fileID := range fileIDs {
		id := fmt.Sprintf("image%d", index)
		text += fmt.Sprintf("\n\n![](tg://photo?id=%s)", id)
		media = append(media, map[string]any{
			"id":    id,
			"media": map[string]any{"type": "photo", "media": fileID},
		})
	}
	return a.call(ctx, token, "editMessageText", map[string]any{
		"inline_message_id": inlineMessageID,
		"rich_message":      map[string]any{"markdown": text, "media": media},
	}, nil)
}

func splitMessage(text string, limit int) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return []string{"Агент не вернул текстовый ответ."}
	}
	runes := []rune(text)
	var out []string
	for len(runes) > limit {
		cut := limit
		for candidate := limit; candidate > limit/2; candidate-- {
			if runes[candidate] == '\n' {
				cut = candidate
				break
			}
		}
		out = append(out, strings.TrimSpace(string(runes[:cut])))
		runes = runes[cut:]
	}
	out = append(out, strings.TrimSpace(string(runes)))
	return out
}
