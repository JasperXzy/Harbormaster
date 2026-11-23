package aliyun

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
)

type PowerObserver struct{ Caller Caller }

// Observe requests only the specified instance and validates the returned
// identity. An empty list is missing, not Stopped; malformed data is unknown.
func (o PowerObserver) Observe(ctx context.Context, cred Credentials, kind, region, id string) (model.PowerObservation, error) {
	var result model.PowerObservation
	action, path := "DescribeInstances", "Instances.Instance"
	if kind == "swas" {
		action, path = "ListInstances", "Instances"
	} else if kind != "ecs" {
		return result, failure("invalid_instance_kind")
	}
	if !powerIDPattern.MatchString(id) {
		return result, failure("invalid_instance_id")
	}
	ids, _ := json.Marshal([]string{id})
	body, err := o.Caller.Call(ctx, cred, action, region, map[string]string{"RegionId": region, "InstanceIds": string(ids), "PageNumber": "1", "PageSize": "10"})
	if err != nil {
		return result, err
	}
	value, ok := valueAt(body, path)
	if !ok {
		return result, failure("invalid_response")
	}
	items, ok := objects(value)
	if !ok {
		return result, failure("invalid_response")
	}
	if len(items) == 0 {
		return result, failure("instance_not_found")
	}
	if len(items) != 1 {
		return result, failure("invalid_response")
	}
	item := items[0]
	if item["InstanceId"] != id || item["RegionId"] != region {
		return result, failure("invalid_response")
	}
	status, _ := item["Status"].(string)
	result.Status = strings.ToLower(status)
	if result.Status == "stoping" {
		result.Status = "stopping"
	}
	result.ObservedAt = time.Now().UTC()
	if kind == "ecs" {
		if raw, ok := item["StoppedMode"].(string); ok {
			mode := map[string]string{"KeepCharging": "keep_charging", "StopCharging": "stop_charging", "Not-applicable": "not_applicable"}[raw]
			if mode != "" {
				result.StoppedMode = &mode
			}
		}
		if raw, ok := item["StartTime"].(string); ok && raw != "" {
			for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04Z"} {
				v, e := time.Parse(layout, raw)
				if e == nil && !v.After(result.ObservedAt) {
					v = v.UTC()
					result.StartedAt = &v
					break
				}
			}
		}
	}
	if !result.Valid() {
		return model.PowerObservation{}, failure("invalid_response")
	}
	return result, nil
}
