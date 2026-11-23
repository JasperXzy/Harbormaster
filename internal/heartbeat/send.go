// Package heartbeat implements external liveness pings without exposing the
// URL, which commonly embeds an authentication token in its path or query.
package heartbeat

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Failure struct{ Code string }

func (f *Failure) Error() string { return f.Code }
func ValidURL(raw string) bool {
	if raw == "" || len(raw) > 2048 || strings.TrimSpace(raw) != raw {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" || u.Opaque != "" {
		return false
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return false
		}
	}
	return true
}

type Sender struct{ client *http.Client }

func NewSender() *Sender {
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	transport := &http.Transport{Proxy: nil, DialContext: dialer.DialContext, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 5 * time.Second, DisableKeepAlives: true}
	return &Sender{client: &http.Client{Transport: transport, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}
func (s *Sender) Send(ctx context.Context, target string) error {
	if !ValidURL(target) {
		return &Failure{Code: "heartbeat_url_invalid"}
	}
	req, err := http.NewRequestWithContext(ctx, "GET", target, nil)
	if err != nil {
		return &Failure{Code: "heartbeat_url_invalid"}
	}
	req.Header.Set("User-Agent", "harbormaster-heartbeat")
	response, err := s.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return &Failure{Code: "heartbeat_cancelled"}
		}
		return &Failure{Code: "heartbeat_unreachable"}
	}
	defer response.Body.Close()
	// Ignore response bodies, which may echo a secret ping URL. Connections are not reused.
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		return nil
	}
	if response.StatusCode == 429 {
		return &Failure{Code: "heartbeat_rate_limited"}
	}
	if response.StatusCode >= 300 && response.StatusCode < 400 {
		return &Failure{Code: "heartbeat_redirect_refused"}
	}
	return &Failure{Code: "heartbeat_http_failed"}
}
func ErrorCode(err error) string {
	if err == nil {
		return ""
	}
	var failure *Failure
	if errors.As(err, &failure) {
		switch failure.Code {
		case "heartbeat_url_invalid", "heartbeat_cancelled", "heartbeat_unreachable", "heartbeat_rate_limited", "heartbeat_redirect_refused", "heartbeat_http_failed":
			return failure.Code
		}
	}
	return "heartbeat_failed"
}
