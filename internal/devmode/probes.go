package devmode

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
	"github.com/JasperXzy/harbormaster/internal/netprobe"
)

type demoProbeCursor struct{ Kind, ID, From, To, After string }

func demoProbePage[T any](w http.ResponseWriter, r *http.Request, items []T, c demoProbeCursor, id func(T) string, descending bool) {
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil || v < 1 || v > 200 {
			failure(w, 422, "validation_failed")
			return
		}
		limit = v
	}
	after := ""
	if raw := r.URL.Query().Get("cursor"); raw != "" {
		if len(raw) > 2048 {
			failure(w, 422, "validation_failed")
			return
		}
		decoded, err := base64.RawURLEncoding.DecodeString(raw)
		var v demoProbeCursor
		if err != nil || json.Unmarshal(decoded, &v) != nil || v.Kind != c.Kind || v.ID != c.ID || v.From != c.From || v.To != c.To || v.After == "" || len(v.After) > 128 {
			failure(w, 422, "validation_failed")
			return
		}
		after = v.After
		if descending {
			n, err := strconv.ParseInt(after, 10, 64)
			if err != nil || n < 1 {
				failure(w, 422, "validation_failed")
				return
			}
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if descending {
			return id(items[i]) > id(items[j])
		}
		return id(items[i]) < id(items[j])
	})
	filtered := []T{}
	for _, item := range items {
		if after == "" || descending && id(item) < after || !descending && id(item) > after {
			filtered = append(filtered, item)
		}
	}
	var next *string
	if len(filtered) > limit {
		filtered = filtered[:limit]
		c.After = id(filtered[len(filtered)-1])
		raw, _ := json.Marshal(c)
		value := base64.RawURLEncoding.EncodeToString(raw)
		next = &value
	}
	respond(w, 200, object{"items": filtered, "next_cursor": next})
}
func demoProbeRun(plan model.ProbePlan, at time.Time) model.ProbeRun {
	job := "demo-probe-job-" + strconv.FormatInt(at.Unix(), 10)
	q := plan.Request("1.1.1.1", 443)
	ms := float64(12 + at.Minute()%7)
	result := &netprobe.Result{StartedAt: at, FinishedAt: at.Add(time.Second), Address: q.Host, ResolvedAddresses: []string{q.Host}, Samples: []netprobe.Sample{{Sequence: 1, StartedAt: at, RTTMS: &ms}}, Summary: &netprobe.Summary{Attempts: 1, Succeeded: 1, MinimumMS: &ms, MaximumMS: &ms, AverageMS: &ms}}
	return model.ProbeRun{ID: strconv.FormatInt(at.Unix(), 10), PlanID: plan.ID, PlanRevision: plan.Revision, TargetID: plan.TargetID, TargetRevision: 1, PointKind: plan.PointKind, InstanceID: plan.InstanceID, CreatedAt: at, UpdatedAt: at.Add(time.Second), Status: "succeeded", JobID: &job, Request: &q, Result: result}
}
func (h *handler) networkProbes(w http.ResponseWriter, r *http.Request, path string, instances []object, now time.Time) bool {
	if !strings.HasPrefix(path, "/network/") {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(path, "/network/"), "/")
	kind := parts[0]
	if kind != "targets" && kind != "plans" && kind != "points" {
		return false
	}
	if kind == "targets" {
		items, err := h.s.NetworkTargets(r.Context(), "", 201)
		if err != nil {
			failure(w, 503, "internal_error")
			return true
		}
		if len(parts) == 1 {
			demoProbePage(w, r, items, demoProbeCursor{Kind: kind}, func(v model.NetworkTarget) string { return v.ID }, false)
			return true
		}
		if len(parts) == 2 {
			for _, item := range items {
				if item.ID == parts[1] {
					respond(w, 200, item)
					return true
				}
			}
		}
		failure(w, 404, "not_found")
		return true
	}
	if kind == "points" && len(parts) == 1 {
		items := []model.ProbePoint{{ID: "server", Name: "hm-server (demo)", PointKind: "server", Available: true}}
		for _, instance := range instances {
			id := instance["id"].(string)
			point := model.ProbePoint{ID: id, Name: instance["name"].(string), PointKind: "agent", InstanceID: &id}
			if instance["agent"].(object)["status"] == "online" {
				point.Available = true
				point.UpdatedAt = &now
			} else {
				reason := "agent_not_registered"
				point.UnavailableReason = &reason
			}
			items = append(items, point)
		}
		demoProbePage(w, r, items, demoProbeCursor{Kind: kind}, func(v model.ProbePoint) string { return v.ID }, false)
		return true
	}
	plans := []model.ProbePlan{}
	for _, point := range []string{"server", "agent"} {
		plan := model.DefaultProbePlan()
		plan.ID = "demo-plan-" + point
		plan.Name = "Probe (demo)"
		plan.PointKind = point
		plan.TargetID = "target_cloudflare_dns"
		plan.Revision = 1
		plan.Count = 1
		plan.Family = "ipv4"
		plan.Method = "tcp"
		plan.Enabled = true
		plan.CreatedAt = now.Add(-time.Hour)
		plan.UpdatedAt = plan.CreatedAt
		next := now.Truncate(5 * time.Minute).Add(5 * time.Minute)
		plan.NextRunAt = &next
		if point == "agent" {
			id := "demo-sin"
			plan.InstanceID = &id
		}
		last := demoProbeRun(plan, now.Truncate(5*time.Minute).Add(-5*time.Minute))
		plan.LastRun = &last
		plans = append(plans, plan)
	}
	if kind == "plans" && len(parts) == 1 {
		demoProbePage(w, r, plans, demoProbeCursor{Kind: kind}, func(v model.ProbePlan) string { return v.ID }, false)
		return true
	}
	for _, plan := range plans {
		if kind != "plans" || len(parts) < 2 || parts[1] != plan.ID {
			continue
		}
		if len(parts) == 2 {
			respond(w, 200, plan)
			return true
		}
		if len(parts) == 3 && parts[2] == "runs" {
			from, e1 := time.Parse(time.RFC3339Nano, r.URL.Query().Get("from"))
			to, e2 := time.Parse(time.RFC3339Nano, r.URL.Query().Get("to"))
			if e1 != nil || e2 != nil || !from.Before(to) || to.Sub(from) > 7*24*time.Hour || to.After(now) {
				failure(w, 422, "validation_failed")
				return true
			}
			runs := []model.ProbeRun{}
			for i := 1; i <= 12; i++ {
				at := now.Truncate(5 * time.Minute).Add(-time.Duration(i) * 5 * time.Minute)
				if !at.Before(from) && at.Before(to) {
					runs = append(runs, demoProbeRun(plan, at))
				}
			}
			demoProbePage(w, r, runs, demoProbeCursor{Kind: "runs", ID: plan.ID, From: from.UTC().Format(time.RFC3339Nano), To: to.UTC().Format(time.RFC3339Nano)}, func(v model.ProbeRun) string { return v.ID }, true)
			return true
		}
	}
	failure(w, 404, "not_found")
	return true
}
