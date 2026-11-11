package aliyun

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
)

func (c Inventory) renewals(ctx context.Context, cred Credentials, region, kind string, instances collection, before func() error) collection {
	if instances.err != nil {
		return collection{err: instances.err}
	}
	ids := []string{}
	for _, row := range instances.rows {
		if charge(row) == "subscription" {
			ids = append(ids, text(row, "InstanceId"))
		}
	}
	sort.Strings(ids)
	action, idParam, pageParam, path, idField := "DescribeInstanceAutoRenewAttribute", "InstanceId", "PageNumber", "InstanceRenewAttributes.InstanceRenewAttribute", "InstanceId"
	if kind == "swas" {
		action, idParam, pageParam, path, idField = "QueryAvailableInstances", "InstanceIDs", "PageNum", "Data.InstanceList", "InstanceID"
	}
	all := []map[string]any{}
	for start := 0; start < len(ids); start += 100 {
		batch := ids[start:min(start+100, len(ids))]
		wanted := map[string]bool{}
		for _, id := range batch {
			wanted[id] = true
		}
		q := map[string]string{idParam: strings.Join(batch, ","), "PageSize": "100"}
		if kind == "swas" {
			q["SubscriptionType"] = "Subscription"
		} else {
			q["RegionId"] = region
		}
		count := 0
		for page := 1; ; page++ {
			if page > 100 {
				return collection{err: failure("page_limit_reached")}
			}
			if err := before(); err != nil {
				return collection{err: err}
			}
			q[pageParam] = strconv.Itoa(page)
			body, err := c.Caller.Call(ctx, cred, action, region, q)
			if err != nil {
				return collection{err: err}
			}
			value, exists := valueAt(body, path)
			rows, ok := objects(value)
			totalBody := body
			if kind == "swas" {
				if body["Success"] != true {
					return collection{err: failure("invalid_response")}
				}
				totalBody = child(body, "Data")
			}
			total, valid := integer(totalBody["TotalCount"])
			if !exists || !ok || !valid || total > int64(len(batch)) {
				return collection{err: failure("invalid_response")}
			}
			for _, row := range rows {
				id := text(row, idField)
				if !wanted[id] {
					return collection{err: failure("invalid_response")}
				}
				if kind == "swas" && (text(row, "Region") != region || text(row, "SubscriptionType") != "Subscription") {
					return collection{err: failure("renewal_identity_mismatch")}
				}
				delete(wanted, id)
				all = append(all, row)
			}
			count += len(rows)
			if int64(count) > total {
				return collection{err: failure("pagination_incomplete")}
			}
			if int64(count) == total {
				break
			}
			if len(rows) == 0 {
				return collection{err: failure("pagination_incomplete")}
			}
		}
	}
	return collection{rows: all}
}

func attachRenewals(snapshots []model.Snapshot, ecs, swas collection, now time.Time) {
	byECS, bySWAS := map[string]map[string]any{}, map[string]map[string]any{}
	for _, row := range ecs.rows {
		byECS[text(row, "InstanceId")] = row
	}
	for _, row := range swas.rows {
		bySWAS[text(row, "InstanceID")] = row
	}
	for _, snapshot := range snapshots {
		if snapshot.Kind != "ecs" && snapshot.Kind != "swas" {
			continue
		}
		i := snapshot.Payload
		view := map[string]any{"status": "unknown", "source": nil, "checked_at": now.Format(time.RFC3339), "last_error": nil}
		i["renewal"] = view
		if i["charge_type"] == "pay_as_you_go" || i["charge_type"] == "spot" {
			view["status"] = "not_applicable"
			continue
		}
		if i["charge_type"] != "subscription" {
			continue
		}
		data, lookup := ecs, byECS
		view["source"] = "ecs_describe_instance_auto_renew_attribute"
		if snapshot.Kind == "swas" {
			data, lookup = swas, bySWAS
			view["source"] = "bss_query_available_instances"
		}
		if data.err != nil {
			view["last_error"] = model.Error(Code(data.err))
			continue
		}
		if row := lookup[text(i, "cloud_instance_id")]; row != nil {
			field := "RenewalStatus"
			if snapshot.Kind == "swas" {
				field = "RenewStatus"
			}
			state := "unknown"
			switch text(row, field) {
			case "AutoRenewal":
				state = "auto_renewal"
			case "Normal":
				if snapshot.Kind == "ecs" {
					state = "manual_renewal"
				}
			case "ManualRenewal":
				if snapshot.Kind == "swas" {
					state = "manual_renewal"
				}
			case "NotRenewal":
				state = "no_renewal"
			}
			if snapshot.Kind == "ecs" {
				if enabled, exists := row["AutoRenewEnabled"]; exists {
					b, ok := enabled.(bool)
					if !ok || (state != "unknown" && b != (state == "auto_renewal")) {
						view["last_error"] = model.Error("renewal_response_conflict")
						state = "unknown"
					}
				}
			}
			view["status"] = state
		}
	}
}
