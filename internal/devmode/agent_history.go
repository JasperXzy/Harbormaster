package devmode

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
)

func demoAgentHistory(w http.ResponseWriter, r *http.Request, path string, instances []object, now time.Time) bool {
	const suffix = "/agent-network-history"
	if !strings.HasPrefix(path, "/instances/") || !strings.HasSuffix(path, suffix) {
		return false
	}
	id := strings.TrimSuffix(strings.TrimPrefix(path, "/instances/"), suffix)
	found := false
	for _, instance := range instances {
		if instance["id"] == id {
			found = true
			break
		}
	}
	if !found {
		failure(w, 404, "not_found")
		return true
	}
	q := r.URL.Query()
	from, e1 := time.Parse(time.RFC3339Nano, q.Get("from"))
	to, e2 := time.Parse(time.RFC3339Nano, q.Get("to"))
	f := model.AgentNetworkHistoryFilter{InstanceID: id, From: from, To: to}
	if e1 != nil || e2 != nil || !f.Valid(now) {
		failure(w, 422, "validation_failed")
		return true
	}
	limit := 500
	if raw := q.Get("limit"); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil || v < 1 || v > 1000 {
			failure(w, 422, "validation_failed")
			return true
		}
		limit = v
	}
	type cursor struct {
		Instance, From, To string
		Offset             int
	}
	expected := cursor{Instance: id, From: from.UTC().Format(time.RFC3339Nano), To: to.UTC().Format(time.RFC3339Nano)}
	offset := 0
	if raw := q.Get("cursor"); raw != "" {
		encoded, err := base64.RawURLEncoding.DecodeString(raw)
		var c cursor
		if len(raw) > 2048 || err != nil || json.Unmarshal(encoded, &c) != nil || c.Instance != expected.Instance || c.From != expected.From || c.To != expected.To || c.Offset < 0 || c.Offset > 12 {
			failure(w, 422, "validation_failed")
			return true
		}
		offset = c.Offset
	}
	items := []model.AgentNetworkSample{}
	for i := 0; i < 12; i++ {
		at := to.UTC().Truncate(10 * time.Second).Add(-time.Duration(i+1) * 10 * time.Second)
		if at.Before(from) {
			continue
		}
		seq := int64(100 - i)
		cadence := 10
		dropped := int64(0)
		rules := "demo-public-v1"
		v := model.AgentNetworkSample{CounterEpoch: "demo-epoch", Sequence: seq, SampledAt: at, ReceivedAt: at.Add(time.Second), UptimeMS: seq * 10000, RXBytes: seq * 1000, TXBytes: seq * 2000, Quality: "ok", RulesVersion: &rules, ReportIntervalSeconds: &cadence, DroppedSamples: &dropped, IntervalStatus: "continuous", Interval: &model.AgentNetworkInterval{PreviousSequence: seq - 1, StartAt: at.Add(-10 * time.Second), EndAt: at, DurationMS: 10000, RXBytes: 1000, TXBytes: 2000, RXRate: 100, TXRate: 200}}
		items = append(items, v)
	}
	if offset > len(items) {
		failure(w, 422, "validation_failed")
		return true
	}
	items = items[offset:]
	var next any
	if len(items) > limit {
		expected.Offset = offset + limit
		raw, _ := json.Marshal(expected)
		next = base64.RawURLEncoding.EncodeToString(raw)
		items = items[:limit]
	}
	respond(w, 200, object{"items": items, "next_cursor": next, "from": from.UTC(), "to": to.UTC()})
	return true
}
