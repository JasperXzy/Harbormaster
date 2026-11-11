package aliyun

import (
	"context"
	"sort"
	"strconv"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
)

// Query only discovered types, at most ten per request as required by ECS.
// Pagination is token-based, unlike the resource inventory's page numbers.
func (c Inventory) instanceTypes(ctx context.Context, cred Credentials, region string, instances collection, before func() error) collection {
	if instances.err != nil {
		return collection{err: instances.err}
	}
	wanted := map[string]bool{}
	for _, row := range instances.rows {
		if id := text(row, "InstanceType"); id != "" {
			wanted[id] = true
		}
	}
	ids := make([]string, 0, len(wanted))
	for id := range wanted {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	all := []map[string]any{}
	for start := 0; start < len(ids); start += 10 {
		batch := ids[start:min(start+10, len(ids))]
		q := map[string]string{"MaxResults": "100"}
		remaining := map[string]bool{}
		for i, id := range batch {
			q["InstanceTypes."+strconv.Itoa(i+1)] = id
			remaining[id] = true
		}
		seenTokens := map[string]bool{}
		for page := 0; ; page++ {
			if page >= 10 {
				return collection{err: failure("page_limit_reached")}
			}
			if err := before(); err != nil {
				return collection{err: err}
			}
			body, err := c.Caller.Call(ctx, cred, "DescribeInstanceTypes", region, q)
			if err != nil {
				return collection{err: err}
			}
			value, exists := valueAt(body, "InstanceTypes.InstanceType")
			rows, ok := objects(value)
			if !exists || !ok {
				return collection{err: failure("invalid_response")}
			}
			for _, row := range rows {
				id := text(row, "InstanceTypeId")
				if !remaining[id] {
					return collection{err: failure("invalid_response")}
				}
				delete(remaining, id)
				all = append(all, row)
			}
			token := ""
			if raw, exists := body["NextToken"]; exists && raw != nil {
				var valid bool
				token, valid = raw.(string)
				if !valid || len(token) > 4096 {
					return collection{err: failure("invalid_response")}
				}
			}
			if token == "" {
				break
			}
			if seenTokens[token] || len(rows) == 0 {
				return collection{err: failure("pagination_incomplete")}
			}
			seenTokens[token] = true
			q["NextToken"] = token
		}
	}
	return collection{rows: all}
}

// Capacity checks use the inventory's resolved outbound billing exit. Public
// inbound limits and actual throughput cannot be inferred from that ceiling.
func attachNetworkCapacity(snapshots []model.Snapshot, types collection, now time.Time) {
	byType := map[string]map[string]any{}
	for _, row := range types.rows {
		byType[text(row, "InstanceTypeId")] = row
	}
	for _, snapshot := range snapshots {
		if snapshot.Kind == "swas" {
			snapshot.Payload["network_capacity"] = nil
			continue
		}
		if snapshot.Kind != "ecs" {
			continue
		}
		i := snapshot.Payload
		capacity := map[string]any{"source": "ecs_describe_instance_types", "checked_at": now.Format(time.RFC3339), "rx_kbps": nil, "tx_kbps": nil, "public_outbound_mbps": nil, "public_outbound_exceeds_spec": nil, "last_error": nil}
		i["network_capacity"] = capacity
		if types.err != nil {
			capacity["last_error"] = model.Error(Code(types.err))
			continue
		}
		row := byType[text(i, "spec")]
		capacity["rx_kbps"] = positiveNumber(row["InstanceBandwidthRx"])
		capacity["tx_kbps"] = positiveNumber(row["InstanceBandwidthTx"])
		// Unknown graph edges must never turn a member EIP's old limit into
		// an effective public ceiling. Multiple exits have no singular ceiling.
		switch i["billing_subject_kind"] {
		case "ecs_fixed_bandwidth", "ecs_pay_by_traffic", "eip_fixed_bandwidth", "eip_pay_by_traffic", "cbwp_bandwidth", "cbwp_dominant_traffic", "cbwp_95":
			capacity["public_outbound_mbps"] = number(i["bandwidth_mbps"])
		}
		if public, ok := capacity["public_outbound_mbps"].(int64); ok {
			if spec, ok := capacity["tx_kbps"].(int64); ok {
				// Integer comparison avoids overflow from public * 1000.
				capacity["public_outbound_exceeds_spec"] = public > spec/1000
			}
		}
	}
}

func positiveNumber(value any) any {
	if n, ok := integer(value); ok && n > 0 {
		return n
	}
	return nil
}
