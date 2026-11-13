package aliyun

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
)

type Inventory struct{ Caller Caller }
type collection struct {
	rows []map[string]any
	err  error
}

func requiredID(c collection, key string) collection {
	if c.err != nil {
		return c
	}
	seen := map[string]bool{}
	for _, row := range c.rows {
		id := text(row, key)
		if id == "" || seen[id] {
			return collection{err: failure("invalid_response")}
		}
		seen[id] = true
	}
	return c
}
func valueAt(body map[string]any, path string) (any, bool) {
	var v any = body
	for _, k := range strings.Split(path, ".") {
		m, ok := v.(map[string]any)
		if !ok {
			return nil, false
		}
		v, ok = m[k]
		if !ok {
			return nil, false
		}
	}
	return v, true
}
func objects(v any) ([]map[string]any, bool) {
	items, ok := v.([]any)
	if !ok {
		return nil, false
	}
	out := []map[string]any{}
	for _, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			return nil, false
		}
		out = append(out, m)
	}
	return out, true
}
func integer(v any) (int64, bool) {
	switch n := v.(type) {
	case int:
		return int64(n), n >= 0
	case int64:
		return n, n >= 0
	case float64:
		if n >= 0 && n <= 1<<53 && float64(int64(n)) == n {
			return int64(n), true
		}
	case string:
		i, e := strconv.ParseInt(n, 10, 64)
		return i, e == nil && i >= 0
	case json.Number:
		i, e := n.Int64()
		return i, e == nil && i >= 0
	}
	return 0, false
}
func (c Inventory) pages(ctx context.Context, cred Credentials, action, region, path string, size int, before func() error) collection {
	all := []map[string]any{}
	for page := 1; page <= 100; page++ {
		if err := before(); err != nil {
			return collection{err: err}
		}
		query := map[string]string{"RegionId": region, "PageNumber": strconv.Itoa(page), "PageSize": strconv.Itoa(size)}
		if action == "DescribeInstances" {
			query["AdditionalAttributes.1"] = "NETWORK_PRIMARY_ENI_IP"
		}
		body, err := c.Caller.Call(ctx, cred, action, region, query)
		if err != nil {
			return collection{err: err}
		}
		v, exists := valueAt(body, path)
		items, ok := objects(v)
		if !exists || !ok {
			return collection{err: failure("invalid_response")}
		}
		all = append(all, items...)
		if raw, exists := body["TotalCount"]; exists {
			total, ok := integer(raw)
			if !ok {
				return collection{err: failure("invalid_response")}
			}
			if int64(len(all)) > total {
				return collection{err: failure("pagination_incomplete")}
			}
			if int64(len(all)) >= total {
				return collection{rows: all}
			}
			if len(items) == 0 {
				return collection{err: failure("pagination_incomplete")}
			}
		} else if len(items) < size {
			return collection{rows: all}
		}
	}
	return collection{err: failure("page_limit_reached")}
}

// Scan continues across independent regions/products. No partial list is
// considered complete, and all before callbacks fence stale/disabled accounts.
func (c Inventory) Scan(ctx context.Context, a model.Account, credential model.Credential, permissions bool, before func() error) model.ScanResult {
	cred := Credentials{Credential: credential, Site: a.Site, Regions: a.RegionIDs}
	result := model.ScanResult{Snapshots: []model.Snapshot{}, CompleteScopes: map[string]bool{}}
	checks := map[string]string{}
	metricRegion, metricID := "", ""
	observe := func(key string, err error) {
		state := "passed"
		if err != nil {
			state = "check_failed"
			if Code(err) == "permission_denied" {
				state = "missing"
			}
			if result.Failure == "" {
				result.Failure = Code(err)
			}
		}
		prev := checks[key]
		if prev == "" || prev == "passed" || state == "missing" {
			checks[key] = state
		}
	}
	call := func(action, region string, q map[string]string) (map[string]any, error) {
		if err := before(); err != nil {
			return nil, err
		}
		return c.Caller.Call(ctx, cred, action, region, q)
	}
	body, err := call("DescribeRegions", a.RegionIDs[0], map[string]string{})
	if err == nil {
		v, ok := valueAt(body, "Regions.Region")
		rows, valid := objects(v)
		if ok && valid {
			for _, r := range rows {
				id := text(r, "RegionId")
				if model.RegionPattern.MatchString(id) {
					result.Snapshots = append(result.Snapshots, model.Snapshot{Kind: "regions", ID: id, Payload: map[string]any{"id": id, "mainland": mainland(id)}})
				}
			}
			result.CompleteScopes[model.Scope("regions", "")] = true
		} else {
			err = failure("invalid_response")
		}
	}
	observe("ecs_read", err)
	for _, region := range a.RegionIDs {
		regionStartedAt := time.Now().UTC()
		if before() != nil || ctx.Err() != nil {
			result.Failure = "account_changed"
			break
		}
		ecs := requiredID(c.pages(ctx, cred, "DescribeInstances", region, "Instances.Instance", 100, before), "InstanceId")
		observe("ecs_read", ecs.err)
		disks := c.releaseDisks(ctx, cred, region, before)
		observe("ecs_read", disks.err)
		result.ReleaseInventory = append(result.ReleaseInventory, releaseInventory(region, ecs, disks, time.Now().UTC()))
		eni := requiredID(c.pages(ctx, cred, "DescribeNetworkInterfaces", region, "NetworkInterfaceSets.NetworkInterfaceSet", 100, before), "NetworkInterfaceId")
		observe("ecs_read", eni.err)
		eips := requiredID(c.pages(ctx, cred, "DescribeEipAddresses", region, "EipAddresses.EipAddress", 100, before), "AllocationId")
		observe("vpc_read", eips.err)
		packages := requiredID(c.pages(ctx, cred, "DescribeCommonBandwidthPackages", region, "CommonBandwidthPackages.CommonBandwidthPackage", 50, before), "BandwidthPackageId")
		observe("vpc_read", packages.err)
		swas := requiredID(c.pages(ctx, cred, "ListInstances", region, "Instances", 100, before), "InstanceId")
		observe("swas_read", swas.err)
		result.Idle = append(result.Idle,
			idleObservation(a, region, "ecs", ecs, ecs, regionStartedAt),
			idleObservation(a, region, "swas", swas, ecs, regionStartedAt),
			idleObservation(a, region, "disk", disks, ecs, regionStartedAt),
			idleObservation(a, region, "eip", eips, ecs, regionStartedAt))
		if len(eips.rows) > 0 && metricID == "" {
			metricID = text(eips.rows[0], "AllocationId")
			metricRegion = region
		}
		trafficObservations, traffic, trafficKnown := c.swasTraffic(ctx, a, cred, region, swas, before, observe)
		result.SWASTraffic = append(result.SWASTraffic, trafficObservations...)
		types := c.instanceTypes(ctx, cred, region, ecs, before)
		observe("ecs_read", types.err)
		ecsRenewal := c.renewals(ctx, cred, region, "ecs", ecs, before)
		observe("ecs_read", ecsRenewal.err)
		swasRenewal := c.renewals(ctx, cred, region, "swas", swas, before)
		if swas.err == nil {
			for _, row := range swas.rows {
				if charge(row) == "subscription" {
					observe("bss_read", swasRenewal.err)
					break
				}
			}
		}
		checkedAt := time.Now().UTC()
		part := normalizeRegion(a, region, ecs, eni, eips, packages, swas, traffic, trafficKnown, checkedAt)
		attachIPv4Quality(part.Snapshots, ecs, eni, eips, swas)
		c.attachIPv6(ctx, cred, region, ecs, part.Snapshots, before, observe)
		attachNetworkCapacity(part.Snapshots, types, checkedAt)
		attachRenewals(part.Snapshots, ecsRenewal, swasRenewal, checkedAt)
		c.attachCPU(ctx, cred, region, part.Snapshots, before, observe)
		c.attachNetworkRates(ctx, cred, region, ecs, part.Snapshots, before, observe)
		result.Snapshots = append(result.Snapshots, part.Snapshots...)
		for k, v := range part.CompleteScopes {
			result.CompleteScopes[k] = v
		}
	}
	if permissions && ctx.Err() == nil {
		if metricID != "" {
			dim, _ := json.Marshal([]map[string]string{{"instanceId": metricID}})
			end := time.Now().UTC()
			_, err := call("DescribeMetricList", metricRegion, map[string]string{"Namespace": "acs_vpc_eip", "MetricName": "net.rx", "Dimensions": string(dim), "StartTime": end.Add(-5 * time.Minute).Format(time.RFC3339), "EndTime": end.Format(time.RFC3339), "Period": "60"})
			observe("cms_read", err)
		}
		// The engine's finance stage checks both balance and bill permissions.
		body, err = call("ListCdtInternetTraffic", "cn-hongkong", map[string]string{"RegionId": "cn-hongkong"})
		observation := model.CDTObservation{AccountID: a.ID, FetchedAt: time.Now().UTC(), Quality: "unknown", Error: model.Error(Code(err))}
		if err == nil {
			observation = parseCDT(a.ID, body, time.Now().UTC())
		}
		result.CDT = &observation
		if Code(err) == "cdt_international_endpoint_unverified" {
			checks["cdt_read"] = "not_verified"
		} else {
			observe("cdt_read", err)
		}
	}
	if permissions {
		items := []model.PermissionItem{}
		for _, key := range []string{"ecs_read", "swas_read", "cdt_read", "cms_read", "instance_power", "vpc_read", "eip_manage", "bss_read"} {
			v := checks[key]
			if v == "" {
				v = "not_verified"
			}
			items = append(items, model.PermissionItem{Key: key, Result: v})
		}
		result.Permissions = &model.PermissionCheck{CheckedAt: time.Now().UTC(), Items: items}
	}
	return result
}
func text(m map[string]any, key string) string { s, _ := m[key].(string); return s }
func mainland(region string) bool              { return strings.HasPrefix(region, "cn-") && region != "cn-hongkong" }
