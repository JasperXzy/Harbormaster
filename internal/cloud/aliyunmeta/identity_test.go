package aliyunmeta

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"testing"
	"time"
)

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func reply(code int, body string) *http.Response {
	return &http.Response{StatusCode: code, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}
}
func TestReadOnlyFixedIMDSv2IdentityAndNoCookies(t *testing.T) {
	paths := []string{"/latest/api/token", "/latest/meta-data/instance-id", "/latest/meta-data/region-id"}
	values := []string{"private-test-metadata-token", "i-test", "cn-shanghai"}
	calls := 0
	jar, _ := cookiejar.New(nil)
	endpoint, _ := url.Parse("http://100.100.100.200/")
	jar.SetCookies(endpoint, []*http.Cookie{{Name: "not-for-metadata", Value: "private-test-cookie"}})
	client := &http.Client{Jar: jar, Timeout: time.Hour, Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		if calls >= 3 || r.URL.Host != "100.100.100.200" || r.URL.Path != paths[calls] || r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" {
			t.Fatal("unexpected metadata request")
		}
		deadline, ok := r.Context().Deadline()
		if !ok || time.Until(deadline) > 5*time.Second {
			t.Fatal("missing total timeout")
		}
		if calls == 0 {
			if r.Method != "PUT" || r.Header.Get("X-aliyun-ecs-metadata-token-ttl-seconds") != "60" {
				t.Fatal("missing v2 token request")
			}
		} else if r.Method != "GET" || r.Header.Get("X-aliyun-ecs-metadata-token") != values[0] {
			t.Fatal("missing metadata authorization")
		}
		result := reply(200, values[calls])
		calls++
		return result, nil
	})}
	identity, err := Read(context.Background(), client)
	if err != nil || !identity.Valid() || identity.InstanceID != "i-test" || calls != 3 {
		t.Fatal(identity, err, calls)
	}
	if client.Jar != jar || client.Timeout != time.Hour {
		t.Fatal("caller client mutated")
	}
}
func TestReadRefusesRedirectEvenWhenCallerAllowsIt(t *testing.T) {
	calls, redirects := 0, 0
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { redirects++; return nil }, Transport: roundTrip(func(*http.Request) (*http.Response, error) {
		calls++
		r := reply(302, "private-test-token")
		r.Header.Set("Location", "http://do-not-follow.invalid/metadata")
		return r, nil
	})}
	identity, err := Read(context.Background(), client)
	if !errors.Is(err, ErrUnavailable) || identity != (Identity{}) || calls != 1 || redirects != 0 {
		t.Fatal(identity, err, calls, redirects)
	}
	_ = client.CheckRedirect(nil, nil)
	if redirects != 1 {
		t.Fatal("caller redirect policy mutated")
	}
}
func TestReadDisablesStandardTransportProxy(t *testing.T) {
	proxies, dials := 0, 0
	transport := &http.Transport{Proxy: func(*http.Request) (*url.URL, error) { proxies++; return url.Parse("http://proxy.invalid:1234") }, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		dials++
		if address != "100.100.100.200:80" {
			t.Fatal("metadata rerouted", address)
		}
		return nil, errors.New("private-test-network-error")
	}}
	_, err := Read(context.Background(), &http.Client{Transport: transport})
	if !errors.Is(err, ErrUnavailable) || proxies != 0 || dials != 1 || strings.Contains(err.Error(), "private-test") {
		t.Fatal("unsafe metadata transport", err, proxies, dials)
	}
	if transport.Proxy == nil || transport.DisableKeepAlives {
		t.Fatal("caller transport mutated")
	}
}
func TestReadRejectsMalformedOrPartialIdentity(t *testing.T) {
	for _, tc := range []struct {
		name   string
		values []string
		status int
	}{
		{"empty token", []string{""}, 200},
		{"large token", []string{strings.Repeat("x", 4097)}, 200},
		{"invalid token header", []string{"token\nprivate"}, 200},
		{"bad instance", []string{"token", "<html>private</html>", "cn-shanghai"}, 200},
		{"bad region", []string{"token", "i-test", "not a region"}, 200},
		{"oversized identity", []string{"token", strings.Repeat("i", 129), "cn-shanghai"}, 200},
		{"metadata unavailable", []string{"private response"}, 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			client := &http.Client{Transport: roundTrip(func(*http.Request) (*http.Response, error) {
				if calls >= len(tc.values) {
					t.Fatal("unexpected fallback request")
				}
				body := tc.values[calls]
				calls++
				return reply(tc.status, body), nil
			})}
			identity, err := Read(context.Background(), client)
			if !errors.Is(err, ErrUnavailable) || identity != (Identity{}) || strings.Contains(err.Error(), "private") {
				t.Fatal(identity, err)
			}
		})
	}
}
func TestReadHonorsShorterCallerDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	client := &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) { <-r.Context().Done(); return nil, r.Context().Err() })}
	identity, err := Read(ctx, client)
	if !errors.Is(err, ErrUnavailable) || identity != (Identity{}) {
		t.Fatal(identity, err)
	}
}
