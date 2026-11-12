package api

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
	"github.com/JasperXzy/harbormaster/internal/netprobe"
	"github.com/JasperXzy/harbormaster/internal/store"
)

var targetInputKeys = []string{"revision", "name", "kind", "host", "instance_id", "tcp_port", "enabled"}
var planInputKeys = []string{"revision", "name", "point_kind", "instance_id", "target_id", "family", "method", "enabled", "frequency_seconds", "count", "timeout_ms", "interval_ms"}

func readProbeInput(w http.ResponseWriter, r *http.Request, out any, keys []string, required int) bool {
	var raw map[string]json.RawMessage
	if !readBody(w, r, &raw) {
		return false
	}
	for i, key := range keys {
		if _, ok := raw[key]; !ok && i < required {
			fieldError(w, key, "required")
			return false
		}
	}
	for key, value := range raw {
		known := false
		for _, allowed := range keys {
			if key == allowed {
				known = true
				break
			}
		}
		if !known || bytes.Equal(value, []byte("null")) && key != "host" && key != "instance_id" && key != "tcp_port" && key != "target_id" && key != "target_revision" {
			fieldError(w, key, "invalid_format")
			return false
		}
	}
	encoded, _ := json.Marshal(raw)
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if decoder.Decode(out) != nil {
		writeError(w, 422, "validation_failed")
		return false
	}
	return true
}
func probeAPIError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, netprobe.ErrInvalid):
		writeError(w, 422, "validation_failed")
	case errors.Is(err, store.ErrNetworkChanged):
		writeError(w, 409, "network_configuration_changed")
	case errors.Is(err, store.ErrTargetInUse):
		writeError(w, 409, "target_in_use")
	default:
		businessError(w, err)
	}
}

type probeCursor struct{ Kind, ID, From, To, After string }

func encodeProbeCursor(c probeCursor) string {
	raw, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(raw)
}
func probePage(w http.ResponseWriter, r *http.Request, expected probeCursor) (int, string, bool) {
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil || v < 1 || v > 200 {
			fieldError(w, "limit", "out_of_range")
			return 0, "", false
		}
		limit = v
	}
	after := ""
	if raw := r.URL.Query().Get("cursor"); raw != "" {
		if len(raw) > 2048 {
			fieldError(w, "cursor", "invalid_format")
			return 0, "", false
		}
		decoded, err := base64.RawURLEncoding.DecodeString(raw)
		var c probeCursor
		if len(raw) > 2048 || err != nil || json.Unmarshal(decoded, &c) != nil || c.Kind != expected.Kind || c.ID != expected.ID || c.From != expected.From || c.To != expected.To || c.After == "" || len(c.After) > 128 {
			fieldError(w, "cursor", "invalid_format")
			return 0, "", false
		}
		after = c.After
	}
	return limit, after, true
}
func probeList[T any](w http.ResponseWriter, items []T, limit int, c probeCursor, id func(T) string) {
	var next *string
	if len(items) > limit {
		items = items[:limit]
		c.After = id(items[len(items)-1])
		value := encodeProbeCursor(c)
		next = &value
	}
	writeJSON(w, 200, map[string]any{"items": items, "next_cursor": next})
}
func (p *productionPanel) networkProbes(w http.ResponseWriter, r *http.Request, path string) bool {
	if p.networkTraceSnapshots(w, r, path) {
		return true
	}
	if p.networkDuplexTraces(w, r, path) {
		return true
	}
	if path == "/network/ipdb" {
		if r.Method != "GET" {
			writeError(w, 405, "method_not_allowed")
		} else if p.ipdb == nil {
			writeError(w, 503, "ipdb_unavailable")
		} else {
			writeJSON(w, 200, p.ipdb.Status())
		}
		return true
	}
	if p.networkTraces(w, r, path) {
		return true
	}
	if !strings.HasPrefix(path, "/network/") {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(path, "/network/"), "/")
	if len(parts) == 3 && parts[0] == "plans" && parts[2] == "runs" {
		return p.probeRuns(w, r, parts[1])
	}
	kind := parts[0]
	if kind != "targets" && kind != "plans" && kind != "points" {
		return false
	}
	if len(parts) > 2 || len(parts) == 2 && parts[1] == "" || kind == "points" && len(parts) != 1 {
		writeError(w, 404, "not_found")
		return true
	}
	id := ""
	if len(parts) == 2 {
		id = parts[1]
	}
	if r.Method == "GET" {
		if id != "" {
			var value any
			var err error
			if kind == "targets" {
				value, err = p.s.NetworkTarget(r.Context(), id)
			} else {
				value, err = p.s.ProbePlan(r.Context(), id)
			}
			if err != nil {
				probeAPIError(w, err)
			} else {
				writeJSON(w, 200, value)
			}
			return true
		}
		cursor := probeCursor{Kind: kind}
		limit, after, ok := probePage(w, r, cursor)
		if !ok {
			return true
		}
		switch kind {
		case "targets":
			items, err := p.s.NetworkTargets(r.Context(), after, limit+1)
			if err != nil {
				probeAPIError(w, err)
			} else {
				probeList(w, items, limit, cursor, func(v model.NetworkTarget) string { return v.ID })
			}
		case "plans":
			items, err := p.s.ProbePlans(r.Context(), after, limit+1)
			if err != nil {
				probeAPIError(w, err)
			} else {
				probeList(w, items, limit, cursor, func(v model.ProbePlan) string { return v.ID })
			}
		case "points":
			items, err := p.s.ProbePoints(r.Context(), after, limit+1, p.now())
			if err != nil {
				probeAPIError(w, err)
			} else {
				probeList(w, items, limit, cursor, func(v model.ProbePoint) string { return v.ID })
			}
		}
		return true
	}
	if kind == "points" {
		writeError(w, 405, "method_not_allowed")
		return true
	}
	if r.Method == "DELETE" && id != "" {
		var in struct {
			Revision int64 `json:"revision"`
		}
		if !readProbeInput(w, r, &in, []string{"revision"}, 1) {
			return true
		}
		var err error
		if kind == "targets" {
			err = p.s.DeleteNetworkTarget(r.Context(), id, in.Revision, p.now())
		} else {
			err = p.s.DeleteProbePlan(r.Context(), id, in.Revision, p.now())
		}
		if err != nil {
			probeAPIError(w, err)
		} else {
			w.WriteHeader(204)
		}
		return true
	}
	if !(r.Method == "POST" && id == "" || r.Method == "PUT" && id != "") {
		writeError(w, 405, "method_not_allowed")
		return true
	}
	status := 200
	if r.Method == "POST" {
		status = 201
	}
	if kind == "targets" {
		var in model.NetworkTarget
		if !readProbeInput(w, r, &in, targetInputKeys, len(targetInputKeys)) {
			return true
		}
		out, err := p.s.SaveNetworkTarget(r.Context(), id, in, p.now())
		if err != nil {
			probeAPIError(w, err)
		} else {
			writeJSON(w, status, out)
		}
	} else {
		in := model.DefaultProbePlan()
		if !readProbeInput(w, r, &in, planInputKeys, 8) {
			return true
		}
		out, err := p.s.SaveProbePlan(r.Context(), id, in, p.now())
		if err != nil {
			probeAPIError(w, err)
		} else {
			writeJSON(w, status, out)
		}
	}
	return true
}
func (p *productionPanel) probeRuns(w http.ResponseWriter, r *http.Request, id string) bool {
	if r.Method != "GET" {
		writeError(w, 405, "method_not_allowed")
		return true
	}
	q := r.URL.Query()
	from, e1 := time.Parse(time.RFC3339Nano, q.Get("from"))
	to, e2 := time.Parse(time.RFC3339Nano, q.Get("to"))
	if e1 != nil || e2 != nil || !from.Before(to) || to.Sub(from) > 7*24*time.Hour || to.After(p.now()) {
		writeError(w, 422, "validation_failed")
		return true
	}
	cursor := probeCursor{Kind: "runs", ID: id, From: from.UTC().Format(time.RFC3339Nano), To: to.UTC().Format(time.RFC3339Nano)}
	limit, after, ok := probePage(w, r, cursor)
	if !ok {
		return true
	}
	var before int64
	if after != "" {
		var err error
		before, err = strconv.ParseInt(after, 10, 64)
		if err != nil || before < 1 {
			fieldError(w, "cursor", "invalid_format")
			return true
		}
	}
	items, err := p.s.ProbeRuns(r.Context(), id, from, to, before, limit+1)
	if err != nil {
		probeAPIError(w, err)
	} else {
		probeList(w, items, limit, cursor, func(v model.ProbeRun) string { return v.ID })
	}
	return true
}
