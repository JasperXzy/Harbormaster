package aliyun

import (
	"context"
	"encoding/json"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
)

type ReleaseObserver struct{ Caller Caller }

// target requires the single-ID filter, exact target/region, and a complete
// count. Failed, malformed or incomplete reads cannot be treated as absence.
func (o ReleaseObserver) target(ctx context.Context, cred Credentials, region, id string) collection {
	if !powerIDPattern.MatchString(id) {
		return collection{err: failure("invalid_instance_id")}
	}
	ids, _ := json.Marshal([]string{id})
	body, err := o.Caller.Call(ctx, cred, "DescribeInstances", region, map[string]string{"RegionId": region, "InstanceIds": string(ids), "PageNumber": "1", "PageSize": "10"})
	if err != nil {
		return collection{err: err}
	}
	raw, exists := valueAt(body, "Instances.Instance")
	rows, ok := objects(raw)
	total, countOK := integer(body["TotalCount"])
	if !exists || !ok || !countOK || total != int64(len(rows)) || len(rows) > 1 {
		return collection{err: failure("invalid_response")}
	}
	if raw, exists := body["NextToken"]; exists {
		token, ok := raw.(string)
		if !ok || token != "" {
			return collection{err: failure("pagination_incomplete")}
		}
	}
	for _, row := range rows {
		if row["InstanceId"] != id || row["RegionId"] != region {
			return collection{err: failure("invalid_response")}
		}
	}
	return collection{rows: rows}
}

func (o ReleaseObserver) Inspect(ctx context.Context, cred Credentials, region, id string) (model.ReleaseInventoryObservation, error) {
	// Starting time conservatively bounds the age of every preflight read.
	at := time.Now().UTC()
	ecs := o.target(ctx, cred, region, id)
	if ecs.err != nil {
		return model.ReleaseInventoryObservation{}, ecs.err
	}
	if len(ecs.rows) == 0 {
		return model.ReleaseInventoryObservation{}, failure("instance_not_found")
	}
	disks := (Inventory{Caller: o.Caller}).releaseDisks(ctx, cred, region, func() error { return ctx.Err() })
	result := releaseInventory(region, ecs, disks, at)
	if result.Error != nil {
		return result, failure(result.Error.Code)
	}
	return result, nil
}

func (o ReleaseObserver) Observe(ctx context.Context, cred Credentials, region, id string) (model.ReleaseObservation, error) {
	rows := o.target(ctx, cred, region, id)
	if rows.err != nil {
		return model.ReleaseObservation{}, rows.err
	}
	return model.ReleaseObservation{InstancePresent: len(rows.rows) != 0, ObservedAt: time.Now().UTC()}, nil
}
