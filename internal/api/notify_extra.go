package api

import (
	"errors"
	"net/http"

	"github.com/JasperXzy/harbormaster/internal/model"
	"github.com/JasperXzy/harbormaster/internal/notify"
	"github.com/JasperXzy/harbormaster/internal/store"
)

const (
	emailTestTitle   = "⚪ 邮件通知测试"
	emailTestBody    = "如果你收到这封邮件，说明 Harbormaster 的 SMTP 邮件通知渠道已经配置正常。"
	webhookTestTitle = "⚪ Webhook 通知测试"
	webhookTestBody  = "如果你在接收端看到这条请求，说明 Harbormaster 的 Webhook 通知渠道已经配置正常。"
)

func (p *productionPanel) extraNotifyChannels(w http.ResponseWriter, r *http.Request, path string) bool {
	switch path {
	case "/notify/email":
		switch r.Method {
		case http.MethodGet:
			p.writeEmailChannel(w, r, 200)
		case http.MethodPut:
			p.updateEmailChannel(w, r)
		default:
			writeError(w, 405, "method_not_allowed")
		}
		return true
	case "/notify/email/test":
		if r.Method != http.MethodPost {
			writeError(w, 405, "method_not_allowed")
			return true
		}
		j, err := p.s.EnqueueEmailTest(r.Context(), emailTestTitle, emailTestBody, p.now().UTC())
		if errors.Is(err, store.ErrEmailNotConfigured) {
			writeError(w, 409, "email_not_configured")
		} else if err != nil {
			businessError(w, err)
		} else {
			writeJSON(w, 202, map[string]string{"job_id": j.ID})
		}
		return true
	case "/notify/webhook":
		switch r.Method {
		case http.MethodGet:
			p.writeWebhookChannel(w, r, 200)
		case http.MethodPut:
			p.updateWebhookChannel(w, r)
		default:
			writeError(w, 405, "method_not_allowed")
		}
		return true
	case "/notify/webhook/test":
		if r.Method != http.MethodPost {
			writeError(w, 405, "method_not_allowed")
			return true
		}
		j, err := p.s.EnqueueWebhookTest(r.Context(), webhookTestTitle, webhookTestBody, p.now().UTC())
		if errors.Is(err, store.ErrWebhookNotConfigured) {
			writeError(w, 409, "webhook_not_configured")
		} else if err != nil {
			businessError(w, err)
		} else {
			writeJSON(w, 202, map[string]string{"job_id": j.ID})
		}
		return true
	case "/notify/daily-report":
		switch r.Method {
		case http.MethodGet:
			dr, err := p.s.DailyReportSettings(r.Context())
			if err != nil {
				businessError(w, err)
			} else {
				writeJSON(w, 200, dr)
			}
		case http.MethodPut:
			var in struct {
				Revision  *int64  `json:"revision"`
				Enabled   *bool   `json:"enabled"`
				TimeOfDay *string `json:"time_of_day"`
			}
			if !readBody(w, r, &in) {
				return true
			}
			if in.Revision == nil || *in.Revision < 0 {
				fieldError(w, "revision", "invalid_format")
				return true
			}
			if in.Enabled == nil {
				fieldError(w, "enabled", "required")
				return true
			}
			if in.TimeOfDay == nil {
				fieldError(w, "time_of_day", "required")
				return true
			}
			out, err := p.s.SaveDailyReportSettings(r.Context(), model.DailyReportSettings{
				Revision:  *in.Revision,
				Enabled:   *in.Enabled,
				TimeOfDay: *in.TimeOfDay,
			}, p.now().UTC())
			switch {
			case err == nil:
				writeJSON(w, 200, out)
			case errors.Is(err, store.ErrChannelChanged):
				writeError(w, 409, "daily_report_changed")
			case err.Error() == "invalid_time_of_day":
				fieldError(w, "time_of_day", "invalid_format")
			default:
				businessError(w, err)
			}
		default:
			writeError(w, 405, "method_not_allowed")
		}
		return true
	case "/notify/daily-report/send":
		if r.Method != http.MethodPost {
			writeError(w, 405, "method_not_allowed")
			return true
		}
		n, err := p.s.TriggerDailyReportNow(r.Context(), p.now().UTC())
		if err != nil {
			businessError(w, err)
		} else {
			writeJSON(w, 202, map[string]string{"notification_id": n.ID})
		}
		return true
	default:
		return false
	}
}

func (p *productionPanel) writeEmailChannel(w http.ResponseWriter, r *http.Request, status int) {
	d, state, err := p.channels().LoadEmail(r.Context())
	ch := model.EmailChannel{
		Enabled:       false,
		Status:        "not_configured",
		Port:          587,
		TLSMode:       "starttls",
		ToAddresses:   []string{},
		SubjectPrefix: "[Harbormaster]",
	}
	if errors.Is(err, store.ErrEmailNotConfigured) {
		writeJSON(w, status, ch)
		return
	}
	if err != nil {
		writeError(w, 503, "email_configuration_unavailable")
		return
	}
	ch.Enabled = state.Enabled
	ch.Revision = state.Revision
	ch.Host = d.Host
	ch.Port = d.Port
	ch.TLSMode = d.TLSMode
	ch.Username = d.Username
	ch.PasswordSet = d.Password != ""
	ch.FromAddress = d.FromAddress
	ch.ToAddresses = d.ToAddresses
	if ch.ToAddresses == nil {
		ch.ToAddresses = []string{}
	}
	ch.SubjectPrefix = d.SubjectPrefix
	ch.LastSuccessAt = state.LastSuccessAt
	ch.LastError = state.LastError
	ch.Status = "not_tested"
	if state.LastError != nil {
		ch.Status = "failing"
	} else if state.LastSuccessAt != nil {
		ch.Status = "ok"
	}
	writeJSON(w, status, ch)
}

func (p *productionPanel) updateEmailChannel(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Enabled       *bool     `json:"enabled"`
		Host          *string   `json:"host"`
		Port          *int      `json:"port"`
		TLSMode       *string   `json:"tls_mode"`
		Username      *string   `json:"username"`
		Password      *string   `json:"password"`
		FromAddress   *string   `json:"from_address"`
		ToAddresses   *[]string `json:"to_addresses"`
		SubjectPrefix *string   `json:"subject_prefix"`
	}
	if !readBody(w, r, &in) {
		return
	}
	for _, f := range []struct {
		name    string
		missing bool
	}{
		{"enabled", in.Enabled == nil},
		{"host", in.Host == nil},
		{"port", in.Port == nil},
		{"tls_mode", in.TLSMode == nil},
		{"from_address", in.FromAddress == nil},
		{"to_addresses", in.ToAddresses == nil},
	} {
		if f.missing {
			fieldError(w, f.name, "required")
			return
		}
	}
	manager := p.channels()
	if manager.Box == nil {
		writeError(w, 503, "email_configuration_unavailable")
		return
	}
	uname := ""
	if in.Username != nil {
		uname = *in.Username
	}
	prefix := "[Harbormaster]"
	if in.SubjectPrefix != nil {
		prefix = *in.SubjectPrefix
	}
	_, err := manager.UpdateEmail(r.Context(), notify.EmailUpdate{
		Enabled:       *in.Enabled,
		Host:          *in.Host,
		Port:          *in.Port,
		TLSMode:       *in.TLSMode,
		Username:      uname,
		Password:      in.Password,
		FromAddress:   *in.FromAddress,
		ToAddresses:   *in.ToAddresses,
		SubjectPrefix: prefix,
	}, p.now().UTC())
	if in.Password != nil {
		*in.Password = ""
	}
	var invalid *notify.FieldError
	switch {
	case errors.As(err, &invalid):
		writeJSON(w, 422, map[string]any{"error": map[string]any{"code": "validation_failed", "message": "validation_failed", "fields": invalid.Fields}})
	case errors.Is(err, store.ErrChannelChanged):
		writeError(w, 409, "email_configuration_changed")
	case errors.Is(err, notify.ErrEmailConfiguration):
		writeError(w, 503, "email_configuration_unavailable")
	case err != nil:
		businessError(w, err)
	default:
		p.writeEmailChannel(w, r, 200)
	}
}

func (p *productionPanel) writeWebhookChannel(w http.ResponseWriter, r *http.Request, status int) {
	d, state, err := p.channels().LoadWebhook(r.Context())
	ch := model.WebhookChannel{
		Enabled: false,
		Status:  "not_configured",
	}
	if errors.Is(err, store.ErrWebhookNotConfigured) {
		writeJSON(w, status, ch)
		return
	}
	if err != nil {
		writeError(w, 503, "webhook_configuration_unavailable")
		return
	}
	ch.Enabled = state.Enabled
	ch.Revision = state.Revision
	ch.URL = d.URL
	ch.SecretSet = d.Secret != ""
	ch.HeaderName = d.HeaderName
	ch.HeaderValueSet = d.HeaderValue != ""
	ch.LastSuccessAt = state.LastSuccessAt
	ch.LastError = state.LastError
	ch.Status = "not_tested"
	if state.LastError != nil {
		ch.Status = "failing"
	} else if state.LastSuccessAt != nil {
		ch.Status = "ok"
	}
	writeJSON(w, status, ch)
}

func (p *productionPanel) updateWebhookChannel(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Enabled     *bool   `json:"enabled"`
		URL         *string `json:"url"`
		Secret      *string `json:"secret"`
		HeaderName  *string `json:"header_name"`
		HeaderValue *string `json:"header_value"`
	}
	if !readBody(w, r, &in) {
		return
	}
	if in.Enabled == nil {
		fieldError(w, "enabled", "required")
		return
	}
	if in.URL == nil {
		fieldError(w, "url", "required")
		return
	}
	manager := p.channels()
	if manager.Box == nil {
		writeError(w, 503, "webhook_configuration_unavailable")
		return
	}
	hn := ""
	if in.HeaderName != nil {
		hn = *in.HeaderName
	}
	_, err := manager.UpdateWebhook(r.Context(), notify.WebhookUpdate{
		Enabled:     *in.Enabled,
		URL:         *in.URL,
		Secret:      in.Secret,
		HeaderName:  hn,
		HeaderValue: in.HeaderValue,
	}, p.now().UTC())
	for _, s := range []*string{in.Secret, in.HeaderValue} {
		if s != nil {
			*s = ""
		}
	}
	var invalid *notify.FieldError
	switch {
	case errors.As(err, &invalid):
		writeJSON(w, 422, map[string]any{"error": map[string]any{"code": "validation_failed", "message": "validation_failed", "fields": invalid.Fields}})
	case errors.Is(err, store.ErrChannelChanged):
		writeError(w, 409, "webhook_configuration_changed")
	case errors.Is(err, notify.ErrWebhookConfiguration):
		writeError(w, 503, "webhook_configuration_unavailable")
	case err != nil:
		businessError(w, err)
	default:
		p.writeWebhookChannel(w, r, 200)
	}
}
