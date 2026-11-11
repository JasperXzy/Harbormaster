package aliyun

import (
	"context"
	"net/netip"
	"sort"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
)

// ipv6Addresses requires an explicit address list on every ENI. Omitted or
// malformed fields must not turn a refresh into evidence that an address vanished.
func ipv6Addresses(rows []map[string]any) ([]string, error) {
	seen := map[string]bool{}
	for _, row := range rows {
		v, exists := valueAt(row, "Ipv6Sets.Ipv6Set")
		items, valid := objects(v)
		if !exists || !valid {
			return nil, failure("ipv6_data_unavailable")
		}
		for _, item := range items {
			ip, err := netip.ParseAddr(text(item, "Ipv6Address"))
			if err != nil || !ip.Is6() || ip.Is4In6() || ip.Zone() != "" {
				return nil, failure("invalid_response")
			}
			seen[ip.String()] = true
		}
	}
	addresses := make([]string, 0, len(seen))
	for address := range seen {
		addresses = append(addresses, address)
	}
	sort.Strings(addresses)
	return addresses, nil
}

func (c Inventory) instanceIPv6(ctx context.Context, cred Credentials, region string, instance map[string]any, before func() error) ([]string, string, error) {
	if v, exists := valueAt(instance, "NetworkInterfaces.NetworkInterface"); exists {
		if rows, valid := objects(v); valid && len(rows) > 0 {
			if checked := requiredID(collection{rows: rows}, "NetworkInterfaceId"); checked.err == nil {
				if addresses, err := ipv6Addresses(rows); err == nil && len(addresses) > 0 {
					return addresses, "ecs_instances", nil
				}
			}
		}
	}
	// One deadline covers rate limiting, every page and HTTP, not three seconds
	// per page. No Type filter: the query must include primary and secondary ENIs.
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	rows, err := c.ipv6Interfaces(ctx, cred, region, text(instance, "InstanceId"), before)
	if ctx.Err() == context.DeadlineExceeded {
		return nil, "", failure("ipv6_lookup_timeout")
	}
	if err != nil {
		return nil, "", err
	}
	addresses, err := ipv6Addresses(rows)
	return addresses, "ecs_network_interfaces", err
}

func (c Inventory) ipv6Interfaces(ctx context.Context, cred Credentials, region, instanceID string, before func() error) ([]map[string]any, error) {
	next := ""
	seenTokens := map[string]bool{}
	seenIDs := map[string]bool{}
	all := []map[string]any{}
	for range 100 {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err := before(); err != nil {
			return nil, err
		}
		query := map[string]string{"RegionId": region, "InstanceId": instanceID, "MaxResults": "100"}
		if next != "" {
			query["NextToken"] = next
		}
		body, err := c.Caller.Call(ctx, cred, "DescribeNetworkInterfaces", region, query)
		if err != nil {
			return nil, err
		}
		v, exists := valueAt(body, "NetworkInterfaceSets.NetworkInterfaceSet")
		rows, valid := objects(v)
		if !exists || !valid {
			return nil, failure("invalid_response")
		}
		for _, row := range rows {
			id := text(row, "NetworkInterfaceId")
			attached := text(row, "InstanceId")
			if attached == "" {
				attached = text(child(row, "Attachment"), "InstanceId")
			}
			if id == "" || seenIDs[id] || attached != instanceID {
				return nil, failure("invalid_response")
			}
			seenIDs[id] = true
		}
		all = append(all, rows...)
		if raw, exists := body["NextToken"]; exists {
			var valid bool
			next, valid = raw.(string)
			if !valid {
				return nil, failure("invalid_response")
			}
		} else {
			next = ""
		}
		if next == "" {
			return all, nil
		}
		if seenTokens[next] || len(rows) == 0 {
			return nil, failure("pagination_incomplete")
		}
		seenTokens[next] = true
	}
	return nil, failure("page_limit_reached")
}

func (c Inventory) attachIPv6(ctx context.Context, cred Credentials, region string, instances collection, snapshots []model.Snapshot, before func() error, observe func(string, error)) {
	byID := map[string]map[string]any{}
	for _, row := range instances.rows {
		byID[text(row, "InstanceId")] = row
	}
	for _, snapshot := range snapshots {
		if snapshot.Kind == "swas" {
			snapshot.Payload["ipv6"] = nil
		}
		if snapshot.Kind != "ecs" {
			continue
		}
		addresses, source, err := c.instanceIPv6(ctx, cred, region, byID[text(snapshot.Payload, "cloud_instance_id")], before)
		observe("ecs_read", err)
		checked := time.Now().UTC().Format(time.RFC3339Nano)
		view := map[string]any{"addresses": addresses, "status": "ok", "source": source, "checked_at": checked, "observed_at": checked, "last_error": nil}
		if err != nil {
			view = map[string]any{"addresses": []string{}, "status": "unknown", "source": nil, "checked_at": checked, "observed_at": nil, "last_error": model.Error(Code(err))}
		}
		snapshot.Payload["ipv6"] = view
	}
}
