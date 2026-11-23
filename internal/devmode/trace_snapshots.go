package devmode

import (
	"net/http"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
)

func (h *handler) demoTraceSnapshots(w http.ResponseWriter, r *http.Request, kind string, plan model.ProbePlan, now time.Time) bool {
	if kind == "trace-schedule" {
		v := model.DefaultTraceSchedule()
		v.PlanID, v.Revision, v.Enabled, v.Active, v.Timezone = plan.ID, 1, true, true, h.loc.String()
		v.UpdatedAt = &plan.CreatedAt
		next, _ := model.NextTraceTime(now, v.TimeOfDay, h.loc)
		v.NextRunAt = &next
		respond(w, 200, v)
		return true
	}
	if kind != "trace-snapshots" {
		return false
	}
	from, e1 := time.Parse(time.RFC3339Nano, r.URL.Query().Get("from"))
	to, e2 := time.Parse(time.RFC3339Nano, r.URL.Query().Get("to"))
	if e1 != nil || e2 != nil || !from.Before(to) || to.Sub(from) > 31*24*time.Hour || to.After(now) {
		failure(w, 422, "validation_failed")
		return true
	}
	index := 0
	if plan.PointKind == "agent" {
		index = 1
	}
	d := demoTraceFixtures(now)[index]
	c := model.PendingTraceComparison()
	c.Status, c.Reason, c.Address = "not_comparable", new("unknown_asn"), &d.Result.Address
	if !d.Outcome.Reached {
		c.Reason = new("measurement_failed")
	}
	run := model.TraceSnapshot{ID: d.ID, PlanID: plan.ID, PlanName: plan.Name, PlanRevision: 1, ScheduleRevision: 1, TargetID: plan.TargetID, TargetRevision: 1, DueAt: d.CreatedAt, CreatedAt: d.CreatedAt, UpdatedAt: d.UpdatedAt, Status: d.Status, TraceID: &d.ID, JobID: &d.JobID, Comparison: c}
	items := []model.TraceSnapshot{}
	if !run.CreatedAt.Before(from) && run.CreatedAt.Before(to) {
		items = append(items, run)
	}
	demoProbePage(w, r, items, demoProbeCursor{Kind: "trace_snapshots", ID: plan.ID, From: from.UTC().Format(time.RFC3339Nano), To: to.UTC().Format(time.RFC3339Nano)}, func(v model.TraceSnapshot) string { return v.ID }, true)
	return true
}
