package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

type InlineButton struct {
	Text         string `json:"text"`
	CallbackData string `json:"callback_data"`
}

type BotUser struct {
	ID int64 `json:"id"`
}

type BotChat struct {
	ID int64 `json:"id"`
}

type BotMessage struct {
	MessageID int64   `json:"message_id"`
	From      BotUser `json:"from"`
	Chat      BotChat `json:"chat"`
	Text      string  `json:"text"`
}

type BotCallbackQuery struct {
	ID      string      `json:"id"`
	From    BotUser     `json:"from"`
	Message *BotMessage `json:"message"`
	Data    string      `json:"data"`
}

type Update struct {
	UpdateID      int64             `json:"update_id"`
	Message       *BotMessage       `json:"message"`
	CallbackQuery *BotCallbackQuery `json:"callback_query"`
}

func methodEndpoint(d Destination, method string) (string, error) {
	ep, err := endpoint(d)
	if err != nil {
		return "", err
	}
	return strings.TrimSuffix(ep, "/sendMessage") + "/" + method, nil
}

func (s Sender) doBotJSON(ctx context.Context, d Destination, method string, payload any, timeout time.Duration, out any) error {
	target, err := methodEndpoint(d, method)
	if err != nil {
		return err
	}
	factory := s.clientFactory
	if factory == nil {
		factory = newClient
	}
	client, err := factory(d)
	if err != nil {
		return invalid()
	}
	if timeout > 0 {
		client.Timeout = timeout
	}
	defer client.CloseIdleConnections()
	raw, err := json.Marshal(payload)
	if err != nil {
		return invalid()
	}
	defer clear(raw)
	req, err := http.NewRequestWithContext(ctx, "POST", target, bytes.NewReader(raw))
	if err != nil {
		return invalid()
	}
	req.Header.Set("Content-Type", "application/json")
	if d.ProxyMode == "reverse_proxy" && d.ProxyUsername != "" {
		req.SetBasicAuth(d.ProxyUsername, d.ProxyPassword)
	}
	res, err := client.Do(req)
	if err != nil {
		return &Failure{Code: "telegram_unreachable", Retryable: true}
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 && res.StatusCode < 400 {
		return &Failure{Code: "telegram_redirect_rejected"}
	}
	if res.StatusCode == 401 {
		return &Failure{Code: "telegram_unauthorized"}
	}
	if res.StatusCode == 403 {
		return &Failure{Code: "telegram_forbidden"}
	}
	if res.StatusCode == 407 {
		return &Failure{Code: "telegram_proxy_unauthorized"}
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, 262145))
	if err != nil || len(body) > 262144 {
		return &Failure{Code: "telegram_response_invalid"}
	}
	var env struct {
		OK     bool            `json:"ok"`
		Result json.RawMessage `json:"result"`
	}
	if json.Unmarshal(body, &env) != nil || !env.OK {
		return &Failure{Code: "telegram_request_rejected"}
	}
	if out != nil {
		if json.Unmarshal(env.Result, out) != nil {
			return &Failure{Code: "telegram_response_invalid"}
		}
	}
	return nil
}

func (s Sender) SendWithButtons(ctx context.Context, d Destination, text string, buttons [][]InlineButton) (Receipt, error) {
	if len(buttons) == 0 {
		return s.Send(ctx, d, text)
	}
	return s.ReplyToChat(ctx, d, d.ChatID, text, buttons)
}

func (s Sender) ReplyToChat(ctx context.Context, d Destination, chatID string, text string, buttons [][]InlineButton) (Receipt, error) {
	if !utf8.ValidString(text) || utf8.RuneCountInString(text) < 1 || utf8.RuneCountInString(text) > 4096 {
		return Receipt{}, &Failure{Code: "telegram_message_invalid"}
	}
	payload := map[string]any{
		"chat_id":              chatID,
		"text":                 text,
		"link_preview_options": map[string]bool{"is_disabled": true},
	}
	if len(buttons) > 0 {
		payload["reply_markup"] = map[string]any{"inline_keyboard": buttons}
	}
	var msg struct {
		ID int64 `json:"message_id"`
	}
	if err := s.doBotJSON(ctx, d, "sendMessage", payload, 15*time.Second, &msg); err != nil {
		return Receipt{}, err
	}
	return Receipt{MessageID: msg.ID}, nil
}

func (s Sender) GetUpdates(ctx context.Context, d Destination, offset int64, timeoutSeconds int) ([]Update, error) {
	if timeoutSeconds < 0 || timeoutSeconds > 25 {
		timeoutSeconds = 5
	}
	payload := map[string]any{
		"offset":          offset,
		"limit":           25,
		"timeout":         timeoutSeconds,
		"allowed_updates": []string{"message", "callback_query"},
	}
	var updates []Update
	if err := s.doBotJSON(ctx, d, "getUpdates", payload, time.Duration(timeoutSeconds+10)*time.Second, &updates); err != nil {
		return nil, err
	}
	return updates, nil
}

func (s Sender) AnswerCallback(ctx context.Context, d Destination, callbackID, text string) error {
	payload := map[string]any{"callback_query_id": callbackID}
	if text != "" {
		payload["text"] = text
	}
	return s.doBotJSON(ctx, d, "answerCallbackQuery", payload, 10*time.Second, nil)
}

func FormatUserID(id int64) string {
	return strconv.FormatInt(id, 10)
}
