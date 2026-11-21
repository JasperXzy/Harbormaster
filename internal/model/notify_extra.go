package model

import (
	"net/mail"
	"net/url"
	"strings"
	"time"
	"unicode"
)

const (
	EmailCredentialOwner   = "notify/email/smtp"
	WebhookCredentialOwner = "notify/webhook/endpoint"
)

type ExtraChannelState struct {
	Enabled       bool
	Revision      int64
	UpdatedAt     time.Time
	LastSuccessAt *time.Time
	LastError     *APIError
}

type EmailDestination struct {
	Host          string   `json:"host"`
	Port          int      `json:"port"`
	TLSMode       string   `json:"tls_mode"` // starttls, implicit_tls, none
	Username      string   `json:"username"`
	Password      string   `json:"password"`
	FromAddress   string   `json:"from_address"`
	ToAddresses   []string `json:"to_addresses"`
	SubjectPrefix string   `json:"subject_prefix"`
}

func (d EmailDestination) FieldErrors() map[string]string {
	f := map[string]string{}
	h := strings.TrimSpace(d.Host)
	if h == "" || len(h) > 255 || strings.IndexFunc(h, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
		f["host"] = "invalid_format"
	}
	if d.Port < 1 || d.Port > 65535 {
		f["port"] = "out_of_range"
	}
	if d.TLSMode != "starttls" && d.TLSMode != "implicit_tls" && d.TLSMode != "none" {
		f["tls_mode"] = "invalid_format"
	}
	if len(d.Username) > 256 || strings.IndexFunc(d.Username, unicode.IsControl) >= 0 {
		f["username"] = "invalid_format"
	}
	if len(d.Password) > 512 || strings.IndexFunc(d.Password, unicode.IsControl) >= 0 {
		f["password"] = "invalid_format"
	}
	if d.Username != "" && d.Password == "" {
		f["password"] = "required"
	}
	if _, err := mail.ParseAddress(strings.TrimSpace(d.FromAddress)); err != nil || len(d.FromAddress) > 320 {
		f["from_address"] = "invalid_format"
	}
	if len(d.ToAddresses) == 0 || len(d.ToAddresses) > 20 {
		f["to_addresses"] = "out_of_range"
	} else {
		for _, addr := range d.ToAddresses {
			if _, err := mail.ParseAddress(strings.TrimSpace(addr)); err != nil || len(addr) > 320 {
				f["to_addresses"] = "invalid_format"
				break
			}
		}
	}
	if len(d.SubjectPrefix) > 64 || strings.IndexFunc(d.SubjectPrefix, unicode.IsControl) >= 0 {
		f["subject_prefix"] = "invalid_format"
	}
	return f
}

type EmailChannel struct {
	Enabled       bool       `json:"enabled"`
	Status        string     `json:"status"` // ok, failing, not_tested, not_configured
	Host          string     `json:"host"`
	Port          int        `json:"port"`
	TLSMode       string     `json:"tls_mode"`
	Username      string     `json:"username"`
	PasswordSet   bool       `json:"password_set"`
	FromAddress   string     `json:"from_address"`
	ToAddresses   []string   `json:"to_addresses"`
	SubjectPrefix string     `json:"subject_prefix"`
	LastSuccessAt *time.Time `json:"last_success_at"`
	LastError     *APIError  `json:"last_error"`
	Revision      int64      `json:"revision"`
}

type WebhookDestination struct {
	URL         string `json:"url"`
	Secret      string `json:"secret"`
	HeaderName  string `json:"header_name"`
	HeaderValue string `json:"header_value"`
}

func (d WebhookDestination) FieldErrors() map[string]string {
	f := map[string]string{}
	u, err := url.Parse(strings.TrimSpace(d.URL))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || len(d.URL) > 1024 || u.User != nil {
		f["url"] = "invalid_format"
	}
	if len(d.Secret) > 512 || strings.IndexFunc(d.Secret, unicode.IsControl) >= 0 {
		f["secret"] = "invalid_format"
	}
	hn := strings.TrimSpace(d.HeaderName)
	if len(hn) > 128 || strings.IndexFunc(hn, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) || r == ':' }) >= 0 {
		f["header_name"] = "invalid_format"
	}
	if strings.EqualFold(hn, "Host") || strings.EqualFold(hn, "Content-Length") || strings.EqualFold(hn, "Transfer-Encoding") {
		f["header_name"] = "invalid_format"
	}
	if len(d.HeaderValue) > 1024 || strings.IndexFunc(d.HeaderValue, unicode.IsControl) >= 0 {
		f["header_value"] = "invalid_format"
	}
	if hn != "" && d.HeaderValue == "" {
		f["header_value"] = "required"
	}
	if hn == "" && d.HeaderValue != "" {
		f["header_name"] = "required"
	}
	return f
}

type WebhookChannel struct {
	Enabled        bool       `json:"enabled"`
	Status         string     `json:"status"` // ok, failing, not_tested, not_configured
	URL            string     `json:"url"`
	SecretSet      bool       `json:"secret_set"`
	HeaderName     string     `json:"header_name"`
	HeaderValueSet bool       `json:"header_value_set"`
	LastSuccessAt  *time.Time `json:"last_success_at"`
	LastError      *APIError  `json:"last_error"`
	Revision       int64      `json:"revision"`
}

type ExtraChannelDelivery struct {
	NotificationRow Notification
	Channel         string
	ChannelRevision int64
	JobID           string
	Attempts        int
	Owner           string
}

type DailyReportSettings struct {
	Enabled      bool       `json:"enabled"`
	TimeOfDay    string     `json:"time_of_day"`
	Revision     int64      `json:"revision"`
	LastSentDate *string    `json:"last_sent_date"`
	UpdatedAt    *time.Time `json:"updated_at"`
}

func DefaultDailyReportSettings() DailyReportSettings {
	return DailyReportSettings{Enabled: false, TimeOfDay: "09:00", Revision: 1}
}
