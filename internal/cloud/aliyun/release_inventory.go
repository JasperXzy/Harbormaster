package aliyun

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
)

func observedBool(row map[string]any, key string) *bool {
	v, ok := row[key].(bool)
	if !ok {
		return nil
	}
	return &v
}

func instanceLocked(row map[string]any) *bool {
	raw, exists := valueAt(row, "OperationLocks.LockReason")
	locks, ok := objects(raw)
	if !exists || !ok {
		return nil
	}
	locked := len(locks) != 0
	return &locked
}

func diskAttachments(row map[string]any) ([]string, bool) {
	ids := map[string]bool{}
	valid := true
	if raw, exists := row["InstanceId"]; exists {
		id, ok := raw.(string)
		valid = valid && ok
		if ok && id != "" {
			ids[id] = true
		}
	}
	lists := false
	for _, path := range []string{"Attachments.Attachment", "MountInstances.MountInstance"} {
		root := strings.Split(path, ".")[0]
		if _, exists := row[root]; !exists {
			continue
		}
		raw, exists := valueAt(row, path)
		items, ok := objects(raw)
		if !exists || !ok {
			valid = false
			continue
		}
		lists = true
		for _, v := range items {
			id := text(v, "InstanceId")
			if id == "" {
				valid = false
			} else {
				ids[id] = true
			}
		}
	}
	status := text(row, "Status")
	multi := text(row, "MultiAttach")
	known := false
	switch status {
	case "Available":
		known = len(ids) == 0
	case "In_use":
		known = len(ids) > 0 && ((multi == "Disabled" && text(row, "InstanceId") != "") || lists)
		if multi == "Disabled" && len(ids) != 1 {
			known = false
		}
	}
	result := make([]string, 0, len(ids))
	for id := range ids {
		result = append(result, id)
	}
	sort.Strings(result)
	return result, known && valid
}

// Token pagination deliberately ignores TotalCount, which DescribeDisks
// documents as invalid when MaxResults/NextToken are used.
func (c Inventory) releaseDisks(ctx context.Context, cred Credentials, region string, before func() error) collection {
	all := []map[string]any{}
	token := ""
	seen := map[string]bool{}
	for page := 0; page < 50; page++ {
		if err := before(); err != nil {
			return collection{err: err}
		}
		if err := ctx.Err(); err != nil {
			return collection{err: err}
		}
		query := map[string]string{"RegionId": region, "MaxResults": "100"}
		if token != "" {
			query["NextToken"] = token
		}
		body, err := c.Caller.Call(ctx, cred, "DescribeDisks", region, query)
		if err != nil {
			return collection{err: err}
		}
		raw, exists := valueAt(body, "Disks.Disk")
		rows, ok := objects(raw)
		if !exists || !ok || len(rows) > 100 {
			return collection{err: failure("invalid_response")}
		}
		all = append(all, rows...)
		next := ""
		if raw, exists := body["NextToken"]; exists {
			var ok bool
			next, ok = raw.(string)
			if !ok || len(next) > 4096 || strings.TrimSpace(next) != next {
				return collection{err: failure("invalid_response")}
			}
		}
		if next == "" {
			return requiredID(collection{rows: all}, "DiskId")
		}
		if seen[next] || len(rows) == 0 {
			return collection{err: failure("pagination_incomplete")}
		}
		seen[next], token = true, next
	}
	return collection{err: failure("page_limit_reached")}
}

func releaseInventory(region string, ecs, disks collection, at time.Time) model.ReleaseInventoryObservation {
	o := model.ReleaseInventoryObservation{Region: region, QueriedAt: at}
	if ecs.err != nil || disks.err != nil {
		err := ecs.err
		if err == nil {
			err = disks.err
		}
		o.Error = model.Error(Code(err))
		return o
	}
	d := &model.ReleaseInventory{Instances: []model.ReleaseInstanceFacts{}, Disks: []model.ReleaseDiskFacts{}}
	for _, row := range ecs.rows {
		if r := text(row, "RegionId"); r != "" && r != region {
			o.Error = model.Error("invalid_response")
			return o
		}
		d.Instances = append(d.Instances, model.ReleaseInstanceFacts{CloudID: text(row, "InstanceId"), Name: text(row, "InstanceName"), Status: text(row, "Status"), ChargeType: text(row, "InstanceChargeType"), SpotStrategy: text(row, "SpotStrategy"), DeletionProtection: observedBool(row, "DeletionProtection"), Locked: instanceLocked(row)})
	}
	for _, row := range disks.rows {
		if r := text(row, "RegionId"); r != "" && r != region {
			o.Error = model.Error("invalid_response")
			return o
		}
		ids, known := diskAttachments(row)
		d.Disks = append(d.Disks, model.ReleaseDiskFacts{CloudID: text(row, "DiskId"), Name: text(row, "DiskName"), Category: text(row, "Category"), Type: text(row, "Type"), InstanceIDs: ids, AttachmentsKnown: known, DeleteWithInstance: observedBool(row, "DeleteWithInstance"), DeleteAutoSnapshot: observedBool(row, "DeleteAutoSnapshot")})
	}
	if !d.Valid() {
		o.Error = model.Error("invalid_response")
		return o
	}
	o.Data = d
	return o
}
