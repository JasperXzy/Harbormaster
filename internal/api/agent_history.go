package api

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
)

type agentHistoryCursor struct {
	InstanceID     string    `json:"instance_id"`
	From           string    `json:"from"`
	To             string    `json:"to"`
	BeforeTime     time.Time `json:"before_time"`
	BeforeEpoch    string    `json:"before_epoch"`
	BeforeSequence int64     `json:"before_sequence"`
}

func (p *productionPanel) agentHistory(w http.ResponseWriter, r *http.Request, path string) bool {
	const suffix = "/agent-network-history"
	if !strings.HasPrefix(path, "/instances/") || !strings.HasSuffix(path, suffix) {
		return false
	}
	if r.Method != "GET" {
		writeError(w, 405, "method_not_allowed")
		return true
	}
	id := strings.TrimSuffix(strings.TrimPrefix(path, "/instances/"), suffix)
	if id == "" || strings.Contains(id, "/") {
		writeError(w, 404, "not_found")
		return true
	}
	q := r.URL.Query()
	from, e1 := time.Parse(time.RFC3339Nano, q.Get("from"))
	to, e2 := time.Parse(time.RFC3339Nano, q.Get("to"))
	f := model.AgentNetworkHistoryFilter{InstanceID: id, From: from.UTC(), To: to.UTC()}
	if e1 != nil || e2 != nil || !f.Valid(p.now().UTC()) {
		writeError(w, 422, "validation_failed")
		return true
	}
	limit := 500
	if raw := q.Get("limit"); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil || v < 1 || v > 1000 {
			fieldError(w, "limit", "out_of_range")
			return true
		}
		limit = v
	}
	if raw := q.Get("cursor"); raw != "" {
		if len(raw) > 2048 {
			fieldError(w, "cursor", "invalid_format")
			return true
		}
		encoded, err := base64.RawURLEncoding.DecodeString(raw)
		var c agentHistoryCursor
		if err != nil || json.Unmarshal(encoded, &c) != nil || c.InstanceID != id || c.From != f.From.Format(time.RFC3339Nano) || c.To != f.To.Format(time.RFC3339Nano) {
			fieldError(w, "cursor", "invalid_format")
			return true
		}
		f.BeforeTime = c.BeforeTime
		f.BeforeEpoch = c.BeforeEpoch
		f.BeforeSequence = c.BeforeSequence
		if c.BeforeTime.IsZero() || !f.Valid(p.now().UTC()) {
			fieldError(w, "cursor", "invalid_format")
			return true
		}
	}
	items, err := p.s.AgentNetworkHistory(r.Context(), f, limit+1)
	if err != nil {
		businessError(w, err)
		return true
	}
	var next any
	if len(items) > limit {
		last := items[limit-1]
		c := agentHistoryCursor{InstanceID: id, From: f.From.Format(time.RFC3339Nano), To: f.To.Format(time.RFC3339Nano), BeforeTime: last.SampledAt, BeforeEpoch: last.CounterEpoch, BeforeSequence: last.Sequence}
		raw, _ := json.Marshal(c)
		next = base64.RawURLEncoding.EncodeToString(raw)
		items = items[:limit]
	}
	writeJSON(w, 200, map[string]any{"items": items, "next_cursor": next, "from": f.From, "to": f.To})
	return true
}
