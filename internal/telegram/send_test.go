package telegram

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"strings"
	"testing"
	"time"
)

const fakeToken = "123456789:TEST_ONLY_not_a_real_bot_token"

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func fakeSender(handler transportFunc) Sender {
	return Sender{clientFactory: func(Destination) (*http.Client, error) {
		return &http.Client{Transport: handler, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, nil
	}}
}
func destination() Destination {
	return Destination{Token: fakeToken, ChatID: "-100123456789", ProxyMode: "none"}
}
func response(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}
}
func TestSendUsesPlainTextWithoutLeakingErrors(t *testing.T) {
	sender := fakeSender(func(r *http.Request) (*http.Response, error) {
		if r.Method != "POST" || r.URL.Scheme != "https" || r.URL.Host != "api.telegram.org" || !strings.HasSuffix(r.URL.Path, "/sendMessage") {
			t.Fatal("unexpected destination")
		}
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) != nil || body["chat_id"] != destination().ChatID || body["text"] != "<not markup>" || body["parse_mode"] != nil {
			t.Fatal("payload changed")
		}
		return response(200, `{"ok":true,"result":{"message_id":7}}`), nil
	})
	receipt, err := sender.Send(context.Background(), destination(), "<not markup>")
	if err != nil || receipt.MessageID != 7 {
		t.Fatal(receipt, err)
	}
	sender = fakeSender(func(r *http.Request) (*http.Response, error) {
		return nil, errors.New("request failed at " + r.URL.String())
	})
	_, err = sender.Send(context.Background(), destination(), "test")
	if strings.Contains(err.Error(), fakeToken) || Classify(err).Uncertain || !Classify(err).Retryable {
		t.Fatal("credential leak or failed connection classification")
	}
}
func TestAmbiguousDeliveryAndRateLimitAreDistinct(t *testing.T) {
	sender := fakeSender(func(r *http.Request) (*http.Response, error) {
		httptrace.ContextClientTrace(r.Context()).WroteHeaders()
		return nil, io.ErrUnexpectedEOF
	})
	_, err := sender.Send(context.Background(), destination(), "test")
	if !Classify(err).Uncertain || Classify(err).Retryable {
		t.Fatal("lost response marked definitely unsent")
	}
	sender = fakeSender(func(*http.Request) (*http.Response, error) {
		return response(429, `{"ok":false,"error_code":429,"parameters":{"retry_after":17},"description":"do not copy this secret `+fakeToken+`"}`), nil
	})
	_, err = sender.Send(context.Background(), destination(), "test")
	if !Classify(err).Retryable || Classify(err).RetryAfter != 17*time.Second || Classify(err).Uncertain || strings.Contains(err.Error(), fakeToken) {
		t.Fatal(err)
	}
}
func TestProxyAuthStaysOnConfiguredHostAndRedirectIsRejected(t *testing.T) {
	d := destination()
	d.ProxyMode = "reverse_proxy"
	d.ProxyURL = "https://proxy.example/telegram"
	d.ProxyUsername = "test-user"
	d.ProxyPassword = "test-password"
	requests := 0
	sender := fakeSender(func(r *http.Request) (*http.Response, error) {
		requests++
		u, p, ok := r.BasicAuth()
		if !ok || u != d.ProxyUsername || p != d.ProxyPassword || r.URL.Host != "proxy.example" || !strings.HasPrefix(r.URL.Path, "/telegram/bot") {
			t.Fatal("proxy authentication misplaced")
		}
		res := response(302, "")
		res.Header.Set("Location", "https://other.example")
		return res, nil
	})
	_, err := sender.Send(context.Background(), d, "test")
	if requests != 1 || Classify(err).Code != "telegram_redirect_rejected" || strings.Contains(err.Error(), d.ProxyPassword) {
		t.Fatal(err)
	}
}
func TestInvalidProxyAndMessageAreRejectedBeforeNetwork(t *testing.T) {
	calls := 0
	sender := fakeSender(func(*http.Request) (*http.Response, error) { calls++; return response(200, `{"ok":true}`), nil })
	for _, raw := range []string{"http://proxy.example", "https://user:pass@proxy.example", "https://proxy.example?secret=token", "https://proxy.example/#fragment"} {
		d := destination()
		d.ProxyMode = "reverse_proxy"
		d.ProxyURL = raw
		if _, err := sender.Send(context.Background(), d, "test"); err == nil {
			t.Fatal(raw)
		}
	}
	if _, err := sender.Send(context.Background(), destination(), strings.Repeat("a", 4097)); err == nil {
		t.Fatal("oversized text accepted")
	}
	if calls != 0 {
		t.Fatal("invalid input reached network")
	}
}

func TestSOCKS5UsesAuthenticationAndRemoteHostname(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	result := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			result <- err
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
		read := func(size int) ([]byte, error) { b := make([]byte, size); _, err := io.ReadFull(conn, b); return b, err }
		greeting, err := read(2)
		if err != nil {
			result <- err
			return
		}
		methods, err := read(int(greeting[1]))
		if err != nil || greeting[0] != 5 || !bytes.Contains(methods, []byte{2}) {
			result <- errors.New("SOCKS authentication not offered")
			return
		}
		if _, err = conn.Write([]byte{5, 2}); err != nil {
			result <- err
			return
		}
		auth, err := read(2)
		if err != nil {
			result <- err
			return
		}
		user, err := read(int(auth[1]))
		if err != nil {
			result <- err
			return
		}
		length, err := read(1)
		if err != nil {
			result <- err
			return
		}
		password, err := read(int(length[0]))
		if err != nil || auth[0] != 1 || string(user) != "test-user" || string(password) != "test-password" {
			result <- errors.New("SOCKS authentication incorrect")
			return
		}
		if _, err = conn.Write([]byte{1, 0}); err != nil {
			result <- err
			return
		}
		request, err := read(5)
		if err != nil || request[0] != 5 || request[1] != 1 || request[3] != 3 {
			result <- errors.New("SOCKS must resolve hostname remotely")
			return
		}
		host, err := read(int(request[4]))
		if err != nil {
			result <- err
			return
		}
		port, err := read(2)
		if err != nil || string(host) != "api.telegram.org" || binary.BigEndian.Uint16(port) != 443 {
			result <- errors.New("wrong SOCKS target")
			return
		}
		_, err = conn.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 0})
		result <- err
	}()
	d := destination()
	d.ProxyMode = "socks5"
	d.ProxyURL = "socks5://" + listener.Addr().String()
	d.ProxyUsername = "test-user"
	d.ProxyPassword = "test-password"
	client, err := newClient(d)
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()
	transport := client.Transport.(*http.Transport)
	if transport.Proxy != nil || transport.TLSClientConfig.MinVersion < tls.VersionTLS12 {
		t.Fatal("unexpected default proxy or TLS policy")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, err := transport.DialContext(ctx, "tcp", "api.telegram.org:443")
	if err != nil {
		t.Fatal("SOCKS dial failed", err)
	}
	conn.Close()
	if err = <-result; err != nil {
		t.Fatal(err)
	}
}
