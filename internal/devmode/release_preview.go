package devmode

import (
	"net/http"
	"strings"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
)

func demoReleasePreview(w http.ResponseWriter, path string, instances []object, now time.Time) bool {
	parts := strings.Split(strings.TrimPrefix(path, "/instances/"), "/")
	if !strings.HasPrefix(path, "/instances/") || len(parts) != 2 || parts[1] != "release-preview" {
		return false
	}
	for _, instance := range instances {
		if instance["id"] != parts[0] {
			continue
		}
		v := model.ReleasePreview{InstanceID: parts[0], AccountID: instance["account_id"].(string), Status: "blocked", BlockReasons: []string{"unsupported_instance_kind"}}
		if instance["kind"] == "ecs" {
			no, yes := false, true
			cloudID := instance["cloud_instance_id"].(string)
			data := model.ReleaseInventory{
				Instances: []model.ReleaseInstanceFacts{{CloudID: cloudID, Name: instance["name"].(string), Status: "Running", ChargeType: "PostPaid", DeletionProtection: &no, Locked: &no}},
				Disks:     []model.ReleaseDiskFacts{{CloudID: "d-example-" + parts[0], Name: "example system disk", Category: "cloud_essd", Type: "system", InstanceIDs: []string{cloudID}, AttachmentsKnown: true, DeleteWithInstance: &yes, DeleteAutoSnapshot: &no}},
			}
			v.Impact, v.BlockReasons = data.ImpactFor(cloudID)
			v.FetchedAt, v.LastAttemptAt = &now, &now
			expiry := now.Add(model.ReleasePreviewLifetime)
			v.ExpiresAt = &expiry
		}
		respond(w, 200, v)
		return true
	}
	failure(w, 404, "not_found")
	return true
}
