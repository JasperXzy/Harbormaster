package heartbeat

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestSenderUsesOneHTTPSGetAndNeverFollowsRedirects(t *testing.T) {
	for _, status := range []int{204, 302, 429, 500} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var calls atomic.Int32
			var server *httptest.Server
			server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Method != "GET" || r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
					t.Error("unexpected credentials or method")
				}
				if r.URL.Path == "/redirected" {
					w.WriteHeader(204)
					return
				}
				w.Header().Set("Location", server.URL+"/redirected")
				w.WriteHeader(status)
				_, _ = w.Write([]byte("opaque-test-token"))
			}))
			defer server.Close()
			sender := NewSender()
			sender.client.Transport = server.Client().Transport
			err := sender.Send(context.Background(), server.URL+"/opaque-test-token")
			if status == 204 && err != nil {
				t.Fatal(err)
			}
			if status != 204 && (err == nil || strings.Contains(err.Error(), "opaque-test-token")) {
				t.Fatal("unsafe or missing error")
			}
			expected := map[int]string{204: "", 302: "heartbeat_redirect_refused", 429: "heartbeat_rate_limited", 500: "heartbeat_http_failed"}[status]
			if ErrorCode(err) != expected {
				t.Fatal("wrong sanitized outcome", ErrorCode(err), expected)
			}
			if calls.Load() != 1 {
				t.Fatal("request repeated", calls.Load())
			}
		})
	}
}
func TestSenderConfigurationAndCancellation(t *testing.T) {
	sender := NewSender()
	transport := sender.client.Transport.(*http.Transport)
	if transport.Proxy != nil || !transport.DisableKeepAlives || transport.TLSClientConfig.InsecureSkipVerify || sender.client.Timeout != 10*time.Second {
		t.Fatal("unexpected transport policy")
	}
	for _, target := range []string{"http://example.org/ping", "https://user:password@example.org/ping", "https://example.org/ping#fragment", "https://example.org:99999/ping", "https://", " https://example.org/ping"} {
		if ValidURL(target) {
			t.Fatal("invalid URL accepted")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := sender.Send(ctx, "https://example.invalid/opaque-test-token")
	if ErrorCode(err) != "heartbeat_cancelled" {
		t.Fatal(err)
	}
	if ErrorCode(errors.New("https://example.invalid/secret")) != "heartbeat_failed" || ErrorCode(&Failure{Code: "secret-url"}) != "heartbeat_failed" {
		t.Fatal("arbitrary error leaked")
	}
}
