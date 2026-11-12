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

func demoNetworkHistory(w http.ResponseWriter, r *http.Request, path string, instances []object, now time.Time) bool {
	const suffix = "/cloud-network-history"
	if !strings.HasPrefix(path, "/instances/") || !strings.HasSuffix(path, suffix) {
		return false
	}
	id := strings.TrimSuffix(strings.TrimPrefix(path, "/instances/"), suffix)
	var instance object
	for _, candidate := range instances {
		if candidate["id"] == id {
			instance = candidate
			break
		}
	}
	if instance == nil {
		failure(w, 404, "not_found")
		return true
	}
	q := r.URL.Query()
	from, e1 := time.Parse(time.RFC3339Nano, q.Get("from"))
	to, e2 := time.Parse(time.RFC3339Nano, q.Get("to"))
	f := model.CloudNetworkHistoryFilter{InstanceID: id, From: from, To: to, Direction: q.Get("direction"), IP: q.Get("ip")}
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
		Instance, From, To, Direction, IP string
		Offset                            int
	}
	expected := cursor{Instance: id, From: from.UTC().Format(time.RFC3339Nano), To: to.UTC().Format(time.RFC3339Nano), Direction: f.Direction, IP: f.IP}
	offset := 0
	if raw := q.Get("cursor"); raw != "" {
		encoded, err := base64.RawURLEncoding.DecodeString(raw)
		var c cursor
		if len(raw) > 2048 || err != nil || json.Unmarshal(encoded, &c) != nil || c.Instance != expected.Instance || c.From != expected.From || c.To != expected.To || c.Direction != expected.Direction || c.IP != expected.IP || c.Offset < 0 || c.Offset > 12 {
			failure(w, 422, "validation_failed")
			return true
		}
		offset = c.Offset
	}
	kind, source := "vpc", "cms"
	var ip *string
	if instance["kind"] == "swas" {
		kind, source = "swas", "swas_describe_monitor_data"
	} else {
		address := instance["public_ips"].([]object)[0]["address"].(string)
		ip = &address
	}
	in, out := model.NetworkMetricNames(kind)
	items := []model.CloudNetworkSample{}
	for i := 0; i < 6; i++ {
		at := to.UTC().Truncate(time.Minute).Add(-time.Duration(i+1) * time.Minute)
		if at.Before(from) {
			continue
		}
		for j, d := range []string{"ingress", "egress"} {
			if f.Direction != "" && f.Direction != d {
				continue
			}
			if f.IP != "" && (ip == nil || *ip != f.IP) {
				continue
			}
			name := in
			if j == 1 {
				name = out
			}
			items = append(items, model.CloudNetworkSample{Source: source, NetworkType: kind, IP: ip, Direction: d, MetricName: name, Unit: "bit/s", PeriodSeconds: 60, Value: float64((i + 1) * (j + 1) * 64000), SampledAt: at, CheckedAt: now})
		}
	}
	if offset > len(items) {
		failure(w, 422, "validation_failed")
		return true
	}
	items = items[offset:]
	var next any
	if len(items) > limit {
		expected.Offset = offset + limit
		b, _ := json.Marshal(expected)
		next = base64.RawURLEncoding.EncodeToString(b)
		items = items[:limit]
	}
	respond(w, 200, object{"items": items, "next_cursor": next, "from": from.UTC(), "to": to.UTC()})
	return true
}
