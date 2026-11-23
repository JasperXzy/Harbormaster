package devmode

import (
	"net/http"
	"strings"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
)

func (h *handler) jobs(w http.ResponseWriter, r *http.Request, path string, now time.Time) bool {
	if path != "/jobs" && !strings.HasPrefix(path, "/jobs/") {
		return false
	}
	items := []object{
		{"id": "demo-job-refresh", "type": "refresh", "status": "succeeded", "created_at": now.Add(-time.Minute).Format(time.RFC3339), "started_at": now.Add(-time.Minute).Format(time.RFC3339), "finished_at": now.Add(-50 * time.Second).Format(time.RFC3339), "error": nil, "result": nil, "account_ids": []string{"demo-account-1", "demo-account-2"}, "instance_id": nil, "trigger": "panel"},
		{"id": "demo-power-job", "type": "instance_power", "status": "failed", "created_at": now.Add(-20 * time.Minute).Format(time.RFC3339), "started_at": nil, "finished_at": now.Add(-19 * time.Minute).Format(time.RFC3339), "error": model.Error("api_key_revoked"), "result": model.PowerJobResult{Action: "stop", StopMode: new("keep_charging"), DispatchOutcome: "not_sent"}, "account_ids": []string{"demo-account-1"}, "instance_id": "demo-sin", "trigger": "api"},
		{"id": "demo-job-permissions", "type": "permission_check", "status": "failed", "created_at": now.Add(-time.Hour).Format(time.RFC3339), "started_at": now.Add(-time.Hour).Format(time.RFC3339), "finished_at": now.Add(-59 * time.Minute).Format(time.RFC3339), "error": object{"code": "permission_denied", "message": "demo placeholder"}, "result": nil, "account_ids": []string{"demo-account-2"}, "instance_id": nil, "trigger": "schedule"},
	}
	if path != "/jobs" {
		for _, item := range items {
			if item["id"] == strings.TrimPrefix(path, "/jobs/") {
				respond(w, 200, item)
				return true
			}
		}
		failure(w, 404, "not_found")
		return true
	}
	q := r.URL.Query()
	f := model.JobFilter{AccountID: q.Get("account_id"), InstanceID: q.Get("instance_id"), Type: q.Get("type"), Status: q.Get("status"), Trigger: q.Get("trigger")}
	if len(f.Validate()) != 0 {
		failure(w, 422, "validation_failed")
		return true
	}
	filtered := []object{}
	for _, item := range items {
		if (f.InstanceID != "" && item["instance_id"] != f.InstanceID) || (f.Type != "" && item["type"] != f.Type) || (f.Status != "" && item["status"] != f.Status) || (f.Trigger != "" && item["trigger"] != f.Trigger) {
			continue
		}
		match := f.AccountID == ""
		for _, id := range item["account_ids"].([]string) {
			match = match || id == f.AccountID
		}
		if match {
			filtered = append(filtered, item)
		}
	}
	page, cursor, err := paginate(filtered, r)
	if err != nil {
		failure(w, 422, "validation_failed")
		return true
	}
	respond(w, 200, object{"items": page, "next_cursor": cursor})
	return true
}
