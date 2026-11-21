package api

import (
	"bufio"
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JasperXzy/harbormaster/internal/collect"
	"github.com/JasperXzy/harbormaster/internal/notify"
	"github.com/JasperXzy/harbormaster/internal/security"
	"github.com/JasperXzy/harbormaster/internal/telegram"
)

type noopTelegramSender struct{}

func (noopTelegramSender) Send(context.Context, telegram.Destination, string) (telegram.Receipt, error) {
	return telegram.Receipt{MessageID: 1}, nil
}

func startMinimalSMTPServer(t *testing.T) (port int, received func() []string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	var mu sync.Mutex
	var msgs []string
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				_, _ = io.WriteString(c, "220 localhost ESMTP\r\n")
				sc := bufio.NewScanner(c)
				inData := false
				var buf strings.Builder
				for sc.Scan() {
					line := sc.Text()
					if inData {
						if line == "." {
							inData = false
							mu.Lock()
							msgs = append(msgs, buf.String())
							mu.Unlock()
							buf.Reset()
							_, _ = io.WriteString(c, "250 2.0.0 OK\r\n")
						} else {
							buf.WriteString(line + "\n")
						}
						continue
					}
					up := strings.ToUpper(line)
					switch {
					case strings.HasPrefix(up, "EHLO"), strings.HasPrefix(up, "HELO"):
						_, _ = io.WriteString(c, "250-localhost\r\n250 AUTH PLAIN\r\n")
					case strings.HasPrefix(up, "AUTH"):
						_, _ = io.WriteString(c, "235 2.7.0 Auth OK\r\n")
					case strings.HasPrefix(up, "MAIL FROM:"), strings.HasPrefix(up, "RCPT TO:"):
						_, _ = io.WriteString(c, "250 2.1.0 OK\r\n")
					case up == "DATA":
						inData = true
						_, _ = io.WriteString(c, "354 End data with <CR><LF>.<CR><LF>\r\n")
					case up == "QUIT":
						_, _ = io.WriteString(c, "221 Bye\r\n")
						return
					default:
						_, _ = io.WriteString(c, "250 OK\r\n")
					}
				}
			}(conn)
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port, func() []string {
		mu.Lock()
		defer mu.Unlock()
		out := make([]string, len(msgs))
		copy(out, msgs)
		return out
	}
}

func TestM4Stage2EmailWebhookAndDailyReport(t *testing.T) {
	p := testPanel(t)
	loc, _ := time.LoadLocation("Asia/Shanghai")
	// 2026-11-21 08:50+08:00 (00:50 UTC) -> before 09:00 daily report
	now := time.Date(2026, 11, 21, 8, 50, 0, 0, loc).UTC()
	p.now = func() time.Time { return now }
	box, _ := security.NewBox(bytes.Repeat([]byte{7}, 32))
	p.engine = &collect.Engine{Store: p.s, Box: box, Scanner: &apiScanner{}, Interval: time.Minute}
	cookie, csrf := setupPanel(t, p)

	smtpPort, smtpReceived := startMinimalSMTPServer(t)

	var whMu sync.Mutex
	var whPayloads [][]byte
	var whSigs, whHeaders []string
	whSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		whMu.Lock()
		whPayloads = append(whPayloads, b)
		whSigs = append(whSigs, r.Header.Get("X-Harbormaster-Signature-256"))
		whHeaders = append(whHeaders, r.Header.Get("X-Custom-Token"))
		whMu.Unlock()
		w.WriteHeader(200)
	}))
	defer whSrv.Close()

	// 1. Save Email (SMTP) channel
	smtpPass := "super-secret-smtp-pass-999"
	w := panelRequest(p, "PUT", "/notify/email", map[string]any{
		"enabled":        true,
		"host":           "127.0.0.1",
		"port":           smtpPort,
		"tls_mode":       "none",
		"username":       "mailer@example.com",
		"password":       smtpPass,
		"from_address":   "harbor@example.com",
		"to_addresses":   []string{"ops@example.com"},
		"subject_prefix": "[HM]",
	}, cookie, csrf)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"password_set":true`) || strings.Contains(w.Body.String(), smtpPass) {
		t.Fatal("save email failed or leaked password", w.Code, w.Body.String())
	}

	// 2. Save Webhook channel
	whSecret := "wh-signing-secret-xyz"
	whTok := "custom-bearer-secret-abc"
	w = panelRequest(p, "PUT", "/notify/webhook", map[string]any{
		"enabled":      true,
		"url":          whSrv.URL + "/hook",
		"secret":       whSecret,
		"header_name":  "X-Custom-Token",
		"header_value": whTok,
	}, cookie, csrf)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"secret_set":true`) || !strings.Contains(w.Body.String(), `"header_value_set":true`) || strings.Contains(w.Body.String(), whSecret) || strings.Contains(w.Body.String(), whTok) {
		t.Fatal("save webhook failed or leaked secret", w.Code, w.Body.String())
	}

	// Verify audit log does not contain secrets
	w = panelRequest(p, "GET", "/audit-log", nil, cookie, "")
	for _, secret := range []string{smtpPass, whSecret, whTok} {
		if strings.Contains(w.Body.String(), secret) {
			t.Fatal("audit log leaked channel secret")
		}
	}

	// 3. Enqueue & dispatch Email test + Webhook test
	w = panelRequest(p, "POST", "/notify/email/test", nil, cookie, csrf)
	var jobResp struct {
		JobID string `json:"job_id"`
	}
	if w.Code != 202 || json.Unmarshal(w.Body.Bytes(), &jobResp) != nil || jobResp.JobID == "" {
		t.Fatal("email test enqueue failed", w.Code, w.Body.String())
	}
	emailJobID := jobResp.JobID

	w = panelRequest(p, "POST", "/notify/webhook/test", nil, cookie, csrf)
	if w.Code != 202 || json.Unmarshal(w.Body.Bytes(), &jobResp) != nil || jobResp.JobID == "" {
		t.Fatal("webhook test enqueue failed", w.Code, w.Body.String())
	}
	webhookJobID := jobResp.JobID

	disp := notify.Dispatcher{
		Channels:      notify.Channels{Store: p.s, Box: box},
		Sender:        noopTelegramSender{},
		WebhookSender: notify.DefaultExtraSender{Client: whSrv.Client()},
		Now:           func() time.Time { return now },
	}
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		worked, err := disp.RunOne(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !worked {
			break
		}
	}

	if len(smtpReceived()) != 1 || !strings.Contains(smtpReceived()[0], "邮件通知测试") {
		t.Fatalf("expected 1 SMTP test message, got %d (%v)", len(smtpReceived()), smtpReceived())
	}
	whMu.Lock()
	if len(whPayloads) != 1 || whHeaders[0] != whTok {
		t.Fatalf("expected 1 webhook test payload, got %d", len(whPayloads))
	}
	mac := hmac.New(sha256.New, []byte(whSecret))
	mac.Write(whPayloads[0])
	wantSig := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if whSigs[0] != wantSig {
		t.Fatalf("webhook HMAC mismatch: got %s want %s", whSigs[0], wantSig)
	}
	whMu.Unlock()

	for _, jid := range []string{emailJobID, webhookJobID} {
		w = panelRequest(p, "GET", "/jobs/"+jid, nil, cookie, "")
		if w.Code != 200 || !strings.Contains(w.Body.String(), `"status":"succeeded"`) {
			t.Fatalf("job %s did not succeed: %s", jid, w.Body.String())
		}
	}

	// 4. Enable Daily Report at 09:00, advance clock to 09:05+08:00, verify scheduled report fans out to BOTH Email and Webhook!
	w = panelRequest(p, "PUT", "/notify/daily-report", map[string]any{
		"revision":    1,
		"enabled":     true,
		"time_of_day": "09:00",
	}, cookie, csrf)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"enabled":true`) {
		t.Fatal("save daily report failed", w.Code, w.Body.String())
	}

	now = time.Date(2026, 11, 21, 9, 5, 0, 0, loc).UTC()
	if err := p.s.EvaluateAlerts(ctx, now); err != nil {
		t.Fatal(err)
	}
	// Second EvaluateAlerts on the same date must be deduplicated
	if err := p.s.EvaluateAlerts(ctx, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 6; i++ {
		worked, err := disp.RunOne(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !worked {
			break
		}
	}

	if got := len(smtpReceived()); got != 2 {
		t.Fatalf("expected 2 total SMTP messages (test + daily report), got %d", got)
	}
	whMu.Lock()
	whCount := len(whPayloads)
	whMu.Unlock()
	if whCount != 2 {
		t.Fatalf("expected 2 total webhook deliveries (test + daily report), got %d ("+strconv.Itoa(whCount)+")", whCount)
	}
}
