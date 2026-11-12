package devmode

import (
	"net/http"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
)

func (h *handler) systemEvents(w http.ResponseWriter, r *http.Request, path string) bool {
	if path != "/system-events" {
		return false
	}
	if r.Method != "GET" {
		failure(w, 405, "method_not_allowed")
		return true
	}
	q := r.URL.Query()
	f := model.SystemEventFilter{AccountID: q.Get("account_id"), InstanceID: q.Get("instance_id"), RegionID: q.Get("region_id"), Source: q.Get("source"), Category: q.Get("category")}
	if !f.Validate() {
		failure(w, 422, "validation_failed")
		return true
	}
	now := time.Now().UTC()
	stamp := now.Format(time.RFC3339)
	items := []object{
		{"id": "demo-cloud-event-1", "account_id": "demo-account-1", "region_id": "ap-southeast-1", "source": "ecs_history", "instance_id": "demo-sin", "cloud_event_id": "e-demo-maintenance", "cloud_instance_id": "i-demo-sin", "category": "maintenance", "event_type": "SystemMaintenance.Reboot", "event_type_code": nil, "provider_status": "Scheduled", "provider_status_code": nil, "published_at": now.Add(-time.Hour).Format(time.RFC3339), "not_before": now.Add(24 * time.Hour).Format(time.RFC3339), "finished_at": nil, "first_seen_at": now.Add(-time.Minute).Format(time.RFC3339), "observed_at": stamp, "seen_in_latest_scan": true},
		{"id": "demo-cloud-event-2", "account_id": "demo-account-1", "region_id": "ap-southeast-1", "source": "cms_spot", "instance_id": "demo-sin", "cloud_event_id": "cms-demo-interruption", "cloud_instance_id": "i-demo-sin", "category": "spot_interruption", "event_type": "Instance:PreemptibleInstanceInterruption", "event_type_code": nil, "provider_status": "Normal", "provider_status_code": nil, "published_at": now.Add(-time.Hour).Format(time.RFC3339), "not_before": nil, "finished_at": nil, "first_seen_at": now.Add(-time.Hour).Format(time.RFC3339), "observed_at": stamp, "seen_in_latest_scan": true},
	}
	for _, field := range []struct{ key, value string }{{"account_id", f.AccountID}, {"instance_id", f.InstanceID}, {"region_id", f.RegionID}, {"source", f.Source}, {"category", f.Category}} {
		items = filter(items, field.key, field.value)
	}
	page, cursor, err := paginate(items, r)
	if err != nil {
		failure(w, 422, "validation_failed")
		return true
	}
	sources := []object{}
	if (f.AccountID == "" || f.AccountID == "demo-account-1") && (f.RegionID == "" || f.RegionID == "ap-southeast-1") && (f.InstanceID == "" || f.InstanceID == "demo-sin") {
		for _, source := range []string{"ecs_history", "cms_spot"} {
			if f.Source == "" || f.Source == source {
				sources = append(sources, object{"account_id": "demo-account-1", "region_id": "ap-southeast-1", "source": source, "status": "ok", "checked_at": stamp, "last_success_at": stamp, "last_error": nil})
			}
		}
	}
	respond(w, 200, object{"items": page, "sources": sources, "next_cursor": cursor})
	return true
}
