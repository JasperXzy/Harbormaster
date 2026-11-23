package devmode

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
)

// Synthetic action evidence only; development reads never alter the journal.
func demoAuditLog(w http.ResponseWriter, r *http.Request, path string, now time.Time) bool {
	if path != "/audit-log" {
		return false
	}
	q := r.URL.Query()
	f := model.AuditFilter{Actor: q.Get("actor"), Action: q.Get("action"), TargetType: q.Get("target_type"), TargetID: q.Get("target_id"), AccountID: q.Get("account_id"), Limit: 50}
	c := demoProbeCursor{Kind: "audit"}
	if q.Has("from") || q.Has("to") {
		from, e1 := time.Parse(time.RFC3339Nano, q.Get("from"))
		to, e2 := time.Parse(time.RFC3339Nano, q.Get("to"))
		if e1 != nil || e2 != nil || to.After(now) {
			failure(w, 422, "validation_failed")
			return true
		}
		f.From, f.To = &from, &to
		c.From, c.To = from.UTC().Format(time.RFC3339Nano), to.UTC().Format(time.RFC3339Nano)
	}
	if !f.Validate() {
		failure(w, 422, "validation_failed")
		return true
	}
	filters, _ := json.Marshal([]string{f.Actor, f.Action, f.TargetType, f.TargetID, f.AccountID})
	c.ID = string(filters)
	account, job := "demo-account-1", "demo-power-job"
	entries := []model.AuditEntry{
		{ID: "1", CreatedAt: now.Add(-time.Hour), Actor: model.AuditActor{Kind: "cli"}, Action: "account_created", TargetType: "account", TargetID: account, AccountID: &account, Details: model.AuditDetails{ChangedFields: []string{"name", "region_ids"}, CredentialsChanged: true, Revision: new(int64(1))}},
		{ID: "2", CreatedAt: now.Add(-30 * time.Minute), Actor: model.AuditActor{Kind: "admin"}, Action: "alert_rule_updated", TargetType: "alert_rule", TargetID: "agent_offline", Details: model.AuditDetails{ChangedFields: []string{"params"}}},
		{ID: "3", CreatedAt: now.Add(-20 * time.Minute), Actor: model.AuditActor{Kind: "api_key", ID: new("demo-key-control")}, Action: "instance_power_requested", TargetType: "instance", TargetID: "demo-sin", AccountID: &account, JobID: &job, Details: model.AuditDetails{ChangedFields: []string{"desired_state"}, Operation: "stop", StopMode: "keep_charging", Status: "queued", DispatchOutcome: "not_sent"}},
		{ID: "4", CreatedAt: now.Add(-19 * time.Minute), Actor: model.AuditActor{Kind: "system"}, Action: "instance_power_finished", TargetType: "instance", TargetID: "demo-sin", AccountID: &account, JobID: &job, Details: model.AuditDetails{ChangedFields: []string{"status"}, Operation: "stop", StopMode: "keep_charging", Status: "failed", DispatchOutcome: "not_sent", ErrorCode: "api_key_revoked"}},
	}
	items := []model.AuditEntry{}
	for _, e := range entries {
		if f.Actor != "" && f.Actor != e.Actor.Kind || f.Action != "" && f.Action != e.Action || f.TargetType != "" && f.TargetType != e.TargetType || f.TargetID != "" && f.TargetID != e.TargetID || f.AccountID != "" && (e.AccountID == nil || *e.AccountID != f.AccountID) || f.From != nil && (e.CreatedAt.Before(*f.From) || !e.CreatedAt.Before(*f.To)) {
			continue
		}
		e.Summary = "audit: " + e.Action + " (demo)"
		items = append(items, e)
	}
	demoProbePage(w, r, items, c, func(v model.AuditEntry) string { return v.ID }, true)
	return true
}
