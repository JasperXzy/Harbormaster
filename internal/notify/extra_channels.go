package notify

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/smtp"
	"strconv"
	"strings"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
	"github.com/JasperXzy/harbormaster/internal/store"
)

var (
	ErrEmailConfiguration   = errors.New("email_configuration_unavailable")
	ErrWebhookConfiguration = errors.New("webhook_configuration_unavailable")
)

type EmailUpdate struct {
	Enabled       bool
	Host          string
	Port          int
	TLSMode       string
	Username      string
	Password      *string // nil = keep saved password
	FromAddress   string
	ToAddresses   []string
	SubjectPrefix string
}

type WebhookUpdate struct {
	Enabled     bool
	URL         string
	Secret      *string // nil = keep saved secret
	HeaderName  string
	HeaderValue *string // nil = keep saved header value
}

func (c Channels) LoadEmail(ctx context.Context) (model.EmailDestination, model.ExtraChannelState, error) {
	sealed, state, err := c.Store.EmailChannel(ctx)
	if err != nil {
		return model.EmailDestination{}, state, err
	}
	if c.Box == nil {
		return model.EmailDestination{}, state, ErrEmailConfiguration
	}
	raw, err := c.Box.Decrypt(sealed, model.EmailCredentialOwner)
	if err != nil {
		return model.EmailDestination{}, state, ErrEmailConfiguration
	}
	defer clear(raw)
	var d model.EmailDestination
	if json.Unmarshal(raw, &d) != nil || len(d.FieldErrors()) > 0 {
		return model.EmailDestination{}, state, ErrEmailConfiguration
	}
	return d, state, nil
}

func (c Channels) UpdateEmail(ctx context.Context, in EmailUpdate, now time.Time) (model.ExtraChannelState, error) {
	if c.Box == nil {
		return model.ExtraChannelState{}, ErrEmailConfiguration
	}
	prev, state, err := c.LoadEmail(ctx)
	configured := err == nil
	if err != nil && !errors.Is(err, store.ErrEmailNotConfigured) {
		return state, err
	}
	cleanTo := make([]string, 0, len(in.ToAddresses))
	for _, a := range in.ToAddresses {
		if t := strings.TrimSpace(a); t != "" {
			cleanTo = append(cleanTo, t)
		}
	}
	prefix := strings.TrimSpace(in.SubjectPrefix)
	if prefix == "" {
		prefix = "[Harbormaster]"
	}
	d := model.EmailDestination{
		Host:          strings.TrimSpace(in.Host),
		Port:          in.Port,
		TLSMode:       strings.TrimSpace(in.TLSMode),
		Username:      strings.TrimSpace(in.Username),
		FromAddress:   strings.TrimSpace(in.FromAddress),
		ToAddresses:   cleanTo,
		SubjectPrefix: prefix,
	}
	if in.Password != nil {
		d.Password = *in.Password
	} else if configured && prev.Host == d.Host && prev.Username == d.Username {
		d.Password = prev.Password
	}
	if fields := d.FieldErrors(); len(fields) > 0 {
		return state, &FieldError{Fields: fields}
	}
	raw, err := json.Marshal(d)
	if err != nil {
		return state, ErrEmailConfiguration
	}
	defer clear(raw)
	sealed, err := c.Box.Encrypt(raw, model.EmailCredentialOwner)
	if err != nil {
		return state, ErrEmailConfiguration
	}
	details := model.AuditDetails{ChangedFields: []string{}}
	for _, f := range []struct {
		name    string
		changed bool
	}{
		{"enabled", state.Enabled != in.Enabled},
		{"host", prev.Host != d.Host},
		{"port", prev.Port != d.Port},
		{"tls_mode", prev.TLSMode != d.TLSMode},
		{"username", prev.Username != d.Username},
		{"from_address", prev.FromAddress != d.FromAddress},
		{"to_addresses", strings.Join(prev.ToAddresses, ",") != strings.Join(d.ToAddresses, ",")},
	} {
		if f.changed {
			details.ChangedFields = append(details.ChangedFields, f.name)
		}
	}
	details.CredentialsChanged = prev.Password != d.Password
	return c.Store.SaveEmailChannel(ctx, in.Enabled, sealed, state.Revision, details, now)
}

func (c Channels) LoadWebhook(ctx context.Context) (model.WebhookDestination, model.ExtraChannelState, error) {
	sealed, state, err := c.Store.WebhookChannel(ctx)
	if err != nil {
		return model.WebhookDestination{}, state, err
	}
	if c.Box == nil {
		return model.WebhookDestination{}, state, ErrWebhookConfiguration
	}
	raw, err := c.Box.Decrypt(sealed, model.WebhookCredentialOwner)
	if err != nil {
		return model.WebhookDestination{}, state, ErrWebhookConfiguration
	}
	defer clear(raw)
	var d model.WebhookDestination
	if json.Unmarshal(raw, &d) != nil || len(d.FieldErrors()) > 0 {
		return model.WebhookDestination{}, state, ErrWebhookConfiguration
	}
	return d, state, nil
}

func (c Channels) UpdateWebhook(ctx context.Context, in WebhookUpdate, now time.Time) (model.ExtraChannelState, error) {
	if c.Box == nil {
		return model.ExtraChannelState{}, ErrWebhookConfiguration
	}
	prev, state, err := c.LoadWebhook(ctx)
	configured := err == nil
	if err != nil && !errors.Is(err, store.ErrWebhookNotConfigured) {
		return state, err
	}
	d := model.WebhookDestination{
		URL:        strings.TrimSpace(in.URL),
		HeaderName: strings.TrimSpace(in.HeaderName),
	}
	if in.Secret != nil {
		d.Secret = *in.Secret
	} else if configured && prev.URL == d.URL {
		d.Secret = prev.Secret
	}
	if in.HeaderValue != nil {
		d.HeaderValue = *in.HeaderValue
	} else if configured && prev.URL == d.URL && prev.HeaderName == d.HeaderName {
		d.HeaderValue = prev.HeaderValue
	}
	if d.HeaderName == "" {
		d.HeaderValue = ""
	}
	if fields := d.FieldErrors(); len(fields) > 0 {
		return state, &FieldError{Fields: fields}
	}
	raw, err := json.Marshal(d)
	if err != nil {
		return state, ErrWebhookConfiguration
	}
	defer clear(raw)
	sealed, err := c.Box.Encrypt(raw, model.WebhookCredentialOwner)
	if err != nil {
		return state, ErrWebhookConfiguration
	}
	details := model.AuditDetails{ChangedFields: []string{}}
	for _, f := range []struct {
		name    string
		changed bool
	}{
		{"enabled", state.Enabled != in.Enabled},
		{"url", prev.URL != d.URL},
		{"header_name", prev.HeaderName != d.HeaderName},
	} {
		if f.changed {
			details.ChangedFields = append(details.ChangedFields, f.name)
		}
	}
	details.CredentialsChanged = prev.Secret != d.Secret || prev.HeaderValue != d.HeaderValue
	return c.Store.SaveWebhookChannel(ctx, in.Enabled, sealed, state.Revision, details, now)
}

type EmailSender interface {
	SendEmail(ctx context.Context, d model.EmailDestination, subject, body string) error
}

type WebhookSender interface {
	SendWebhook(ctx context.Context, d model.WebhookDestination, n model.Notification) error
}

type DefaultExtraSender struct {
	Client *http.Client
}

func (s DefaultExtraSender) SendEmail(ctx context.Context, d model.EmailDestination, subject, body string) error {
	addr := net.JoinHostPort(d.Host, strconv.Itoa(d.Port))
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	var conn net.Conn
	var err error
	if d.TLSMode == "implicit_tls" {
		tlsDialer := &tls.Dialer{NetDialer: dialer, Config: &tls.Config{ServerName: d.Host, MinVersion: tls.VersionTLS12}}
		conn, err = tlsDialer.DialContext(ctx, "tcp", addr)
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return errors.New("smtp_unreachable")
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	} else {
		_ = conn.SetDeadline(time.Now().Add(15 * time.Second))
	}
	client, err := smtp.NewClient(conn, d.Host)
	if err != nil {
		_ = conn.Close()
		return errors.New("smtp_handshake_failed")
	}
	defer client.Close()

	if d.TLSMode == "starttls" {
		if err = client.StartTLS(&tls.Config{ServerName: d.Host, MinVersion: tls.VersionTLS12}); err != nil {
			return errors.New("smtp_starttls_failed")
		}
	}
	if d.Username != "" {
		auth := smtp.PlainAuth("", d.Username, d.Password, d.Host)
		if err = client.Auth(auth); err != nil {
			return errors.New("smtp_auth_failed")
		}
	}
	if err = client.Mail(d.FromAddress); err != nil {
		return errors.New("smtp_sender_rejected")
	}
	for _, rcpt := range d.ToAddresses {
		if err = client.Rcpt(rcpt); err != nil {
			return errors.New("smtp_recipient_rejected")
		}
	}
	wc, err := client.Data()
	if err != nil {
		return errors.New("smtp_data_failed")
	}
	encodedSubj := mime.QEncoding.Encode("utf-8", subject)
	var msg strings.Builder
	msg.WriteString("From: " + d.FromAddress + "\r\n")
	msg.WriteString("To: " + strings.Join(d.ToAddresses, ", ") + "\r\n")
	msg.WriteString("Subject: " + encodedSubj + "\r\n")
	msg.WriteString("Date: " + time.Now().UTC().Format(time.RFC1123Z) + "\r\n")
	msg.WriteString("MIME-Version: 1.0\r\n")
	msg.WriteString("Content-Type: text/plain; charset=UTF-8\r\n\r\n")
	msg.WriteString(strings.ReplaceAll(body, "\n", "\r\n") + "\r\n")
	if _, err = io.WriteString(wc, msg.String()); err != nil {
		_ = wc.Close()
		return errors.New("smtp_write_failed")
	}
	if err = wc.Close(); err != nil {
		return errors.New("smtp_commit_failed")
	}
	_ = client.Quit()
	return nil
}

func (s DefaultExtraSender) SendWebhook(ctx context.Context, d model.WebhookDestination, n model.Notification) error {
	payload := map[string]any{
		"id":          n.ID,
		"event_key":   n.EventKey,
		"kind":        n.Kind,
		"severity":    n.Severity,
		"rule_key":    n.RuleKey,
		"title":       n.Title,
		"body":        n.Body,
		"account_id":  n.AccountID,
		"instance_id": n.InstanceID,
		"created_at":  n.CreatedAt.UTC().Format(time.RFC3339Nano),
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return errors.New("webhook_encode_failed")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.URL, bytes.NewReader(raw))
	if err != nil {
		return errors.New("webhook_invalid_url")
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("User-Agent", "harbormaster-webhook")
	if d.HeaderName != "" && d.HeaderValue != "" {
		req.Header.Set(d.HeaderName, d.HeaderValue)
	}
	if d.Secret != "" {
		mac := hmac.New(sha256.New, []byte(d.Secret))
		mac.Write(raw)
		req.Header.Set("X-Harbormaster-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	}
	client := s.Client
	if client == nil {
		client = &http.Client{
			Timeout: 15 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
	}
	resp, err := client.Do(req)
	if err != nil {
		return errors.New("webhook_unreachable")
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
		return fmt.Errorf("webhook_http_%d_retryable", resp.StatusCode)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("webhook_http_%d", resp.StatusCode)
	}
	return nil
}

func (d Dispatcher) runExtraChannelOne(ctx context.Context) (bool, error) {
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	s := d.Channels.Store
	if s == nil {
		return false, nil
	}
	item, err := s.ClaimExtraChannelDelivery(ctx, d.now(), MaxAttempts)
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	finish := func(code string, retryAt *time.Time) error {
		fCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer stop()
		e := s.FinishExtraChannelDelivery(fCtx, item, code, retryAt, d.now())
		if errors.Is(e, store.ErrLeaseLost) {
			return nil
		}
		return e
	}

	sendCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	var sendErr error
	retryable := false
	switch item.Channel {
	case "email":
		dest, state, lErr := d.Channels.LoadEmail(ctx)
		if lErr != nil {
			return true, finish("email_configuration_unavailable", nil)
		}
		if state.Revision != item.ChannelRevision {
			return true, finish("email_configuration_changed", nil)
		}
		emailSender := d.EmailSender
		if emailSender == nil {
			emailSender = DefaultExtraSender{}
		}
		subj := strings.TrimSpace(dest.SubjectPrefix + " " + item.NotificationRow.Title)
		sendErr = emailSender.SendEmail(sendCtx, dest, subj, Render(item.NotificationRow))
		if sendErr != nil && sendErr.Error() == "smtp_unreachable" {
			retryable = true
		}
	case "webhook":
		dest, state, lErr := d.Channels.LoadWebhook(ctx)
		if lErr != nil {
			return true, finish("webhook_configuration_unavailable", nil)
		}
		if state.Revision != item.ChannelRevision {
			return true, finish("webhook_configuration_changed", nil)
		}
		whSender := d.WebhookSender
		if whSender == nil {
			whSender = DefaultExtraSender{}
		}
		sendErr = whSender.SendWebhook(sendCtx, dest, item.NotificationRow)
		if sendErr != nil && (sendErr.Error() == "webhook_unreachable" || strings.HasSuffix(sendErr.Error(), "_retryable")) {
			retryable = true
		}
	default:
		return true, finish("unsupported_channel", nil)
	}

	if sendErr == nil {
		return true, finish("", nil)
	}
	code := strings.TrimSuffix(sendErr.Error(), "_retryable")
	if retryable && item.Attempts < MaxAttempts {
		delay := retryDelays[min(item.Attempts-1, len(retryDelays)-1)]
		at := d.now().Add(delay)
		return true, finish(code, &at)
	}
	return true, finish(code, nil)
}
