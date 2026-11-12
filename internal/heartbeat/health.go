package heartbeat

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"time"
)

const SchedulerGrace = 30 * time.Second

type HealthGate struct {
	ReadyURL string
	Progress func() time.Time
	client   *http.Client
}

func NewHealthGate(readyURL string, progress func() time.Time) *HealthGate {
	d := &net.Dialer{Timeout: time.Second}
	return &HealthGate{ReadyURL: readyURL, Progress: progress, client: &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{Proxy: nil, DialContext: d.DialContext, DisableKeepAlives: true}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}
func (g *HealthGate) Healthy(ctx context.Context) bool {
	if g.Progress == nil {
		return false
	}
	at := g.Progress()
	now := time.Now()
	if at.IsZero() || at.After(now) || now.Sub(at) > SchedulerGrace {
		return false
	}
	req, err := http.NewRequestWithContext(ctx, "GET", g.ReadyURL, nil)
	if err != nil {
		return false
	}
	response, err := g.client.Do(req)
	if err != nil {
		return false
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return false
	}
	var body struct {
		Status string `json:"status"`
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 256))
	return decoder.Decode(&body) == nil && body.Status == "ready" && decoder.Decode(new(any)) == io.EOF
}
