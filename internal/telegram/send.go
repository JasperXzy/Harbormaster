// Package telegram is a credential-safe Telegram Bot API transport. It never
// logs requests, response descriptions or HTTP errors containing token URLs.
package telegram

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"regexp"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"golang.org/x/net/proxy"
)

type Destination struct {
	Token, ChatID                string
	ProxyMode, ProxyURL          string
	ProxyUsername, ProxyPassword string
}
type Failure struct {
	Code       string
	Retryable  bool
	RetryAfter time.Duration
	Uncertain  bool
}

func (f *Failure) Error() string { return f.Code }

type Receipt struct{ MessageID int64 }
type Sender struct {
	clientFactory func(Destination) (*http.Client, error)
}

var tokenPattern = regexp.MustCompile(`^[0-9]{1,20}:[A-Za-z0-9_-]{20,128}$`)
var chatPattern = regexp.MustCompile(`^(?:-?[0-9]{1,20}|@[A-Za-z0-9_]{5,64})$`)

func invalid() *Failure { return &Failure{Code: "telegram_configuration_invalid"} }
func endpoint(d Destination) (string, error) {
	if !tokenPattern.MatchString(d.Token) || !chatPattern.MatchString(d.ChatID) {
		return "", invalid()
	}
	base := "https://api.telegram.org"
	switch d.ProxyMode {
	case "none", "":
		if d.ProxyURL != "" || d.ProxyUsername != "" || d.ProxyPassword != "" {
			return "", invalid()
		}
	case "reverse_proxy":
		u, err := url.Parse(d.ProxyURL)
		if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" {
			return "", invalid()
		}
		base = strings.TrimRight(u.String(), "/")
	case "socks5":
		u, err := url.Parse(d.ProxyURL)
		if err != nil || u.Scheme != "socks5" || u.Hostname() == "" || u.Port() == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
			return "", invalid()
		}
	default:
		return "", invalid()
	}
	if len(d.ProxyUsername) > 255 || len(d.ProxyPassword) > 255 || (d.ProxyUsername == "") != (d.ProxyPassword == "") {
		return "", invalid()
	}
	return base + "/bot" + d.Token + "/sendMessage", nil
}
func newClient(d Destination) (*http.Client, error) {
	dialer := &net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}
	transport := &http.Transport{Proxy: nil, DialContext: dialer.DialContext, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 10 * time.Second}
	if d.ProxyMode == "socks5" {
		u, err := url.Parse(d.ProxyURL)
		if err != nil {
			return nil, invalid()
		}
		var auth *proxy.Auth
		if d.ProxyUsername != "" {
			auth = &proxy.Auth{User: d.ProxyUsername, Password: d.ProxyPassword}
		}
		socks, err := proxy.SOCKS5("tcp", u.Host, auth, dialer)
		if err != nil {
			return nil, invalid()
		}
		contextual, ok := socks.(proxy.ContextDialer)
		if !ok {
			return nil, invalid()
		}
		transport.DialContext = contextual.DialContext
	}
	return &http.Client{Transport: transport, Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, nil
}
func (s Sender) Send(ctx context.Context, d Destination, text string) (Receipt, error) {
	target, err := endpoint(d)
	if err != nil {
		return Receipt{}, err
	}
	if !utf8.ValidString(text) || utf8.RuneCountInString(text) < 1 || utf8.RuneCountInString(text) > 4096 {
		return Receipt{}, &Failure{Code: "telegram_message_invalid"}
	}
	factory := s.clientFactory
	if factory == nil {
		factory = newClient
	}
	client, err := factory(d)
	if err != nil {
		return Receipt{}, invalid()
	}
	defer client.CloseIdleConnections()
	raw, _ := json.Marshal(map[string]any{"chat_id": d.ChatID, "text": text, "link_preview_options": map[string]bool{"is_disabled": true}})
	defer clear(raw)
	var wrote atomic.Bool
	trace := &httptrace.ClientTrace{WroteHeaders: func() { wrote.Store(true) }}
	req, err := http.NewRequestWithContext(httptrace.WithClientTrace(ctx, trace), "POST", target, bytes.NewReader(raw))
	if err != nil {
		return Receipt{}, invalid()
	}
	req.Header.Set("Content-Type", "application/json")
	if d.ProxyMode == "reverse_proxy" && d.ProxyUsername != "" {
		req.SetBasicAuth(d.ProxyUsername, d.ProxyPassword)
	}
	response, err := client.Do(req)
	if err != nil {
		return Receipt{}, &Failure{Code: "telegram_unreachable", Retryable: !wrote.Load(), Uncertain: wrote.Load()}
	}
	defer response.Body.Close()
	// Redirects are never followed, since the URL contains the bot credential.
	if response.StatusCode >= 300 && response.StatusCode < 400 {
		return Receipt{}, &Failure{Code: "telegram_redirect_rejected"}
	}
	if response.StatusCode == 407 {
		return Receipt{}, &Failure{Code: "telegram_proxy_unauthorized"}
	}
	if response.StatusCode == 401 {
		return Receipt{}, &Failure{Code: "telegram_unauthorized"}
	}
	if response.StatusCode == 403 {
		return Receipt{}, &Failure{Code: "telegram_forbidden"}
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 65537))
	if err != nil || len(body) > 65536 {
		return Receipt{}, &Failure{Code: "telegram_response_invalid", Uncertain: true}
	}
	var result struct {
		OK          bool   `json:"ok"`
		ErrorCode   int    `json:"error_code"`
		Description string `json:"description"`
		Result      struct {
			ID int64 `json:"message_id"`
		} `json:"result"`
		Parameters struct {
			RetryAfter int   `json:"retry_after"`
			MigrateTo  int64 `json:"migrate_to_chat_id"`
		} `json:"parameters"`
	}
	if json.Unmarshal(body, &result) != nil {
		return Receipt{}, &Failure{Code: "telegram_response_invalid", Uncertain: true}
	}
	if response.StatusCode == 200 && result.OK && result.Result.ID > 0 {
		return Receipt{MessageID: result.Result.ID}, nil
	}
	if !result.OK && result.ErrorCode == 429 && result.Parameters.RetryAfter > 0 {
		return Receipt{}, &Failure{Code: "telegram_rate_limited", Retryable: true, RetryAfter: time.Duration(min(result.Parameters.RetryAfter, 86400)) * time.Second}
	}
	if !result.OK && result.Parameters.MigrateTo != 0 {
		return Receipt{}, &Failure{Code: "telegram_chat_migrated"}
	}
	if !result.OK && result.ErrorCode == 400 {
		if strings.Contains(strings.ToLower(result.Description), "chat not found") {
			return Receipt{}, &Failure{Code: "telegram_chat_not_found"}
		}
		return Receipt{}, &Failure{Code: "telegram_request_rejected"}
	}
	// A broken proxy response or a request whose response was lost cannot prove
	// non-delivery. The outbox must decide its retry policy explicitly.
	return Receipt{}, &Failure{Code: "telegram_response_invalid", Uncertain: true}
}
func Classify(err error) *Failure {
	var failure *Failure
	if errors.As(err, &failure) {
		return failure
	}
	return &Failure{Code: "telegram_unreachable", Uncertain: true}
}
