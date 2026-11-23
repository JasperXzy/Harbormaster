package aliyun

import (
	"net/netip"
	"sort"
	"strings"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
)

func resourceID(a model.Account, kind, region, id string) string {
	return a.ID + "_" + kind + "_" + region + "_" + id
}
func nullableText(s string) any {
	if s == "" {
		return nil
	}
	return s
}
func number(v any) any {
	if n, ok := integer(v); ok {
		return n
	}
	return nil
}
func utcTime(v string) any {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04Z"} {
		if t, err := time.Parse(layout, v); err == nil {
			return t.UTC().Format(time.RFC3339)
		}
	}
	return nil
}
func ipAddress(s string) string {
	ip, err := netip.ParseAddr(s)
	if err != nil || !ip.Is4() {
		return ""
	}
	return ip.String()
}
func child(m map[string]any, key string) map[string]any { v, _ := m[key].(map[string]any); return v }
func status(v string) string {
	switch strings.ToLower(v) {
	case "pending":
		return "pending"
	case "starting":
		return "starting"
	case "running":
		return "running"
	case "stopping":
		return "stopping"
	case "stopped":
		return "stopped"
	default:
		return "unknown"
	}
}
func charge(m map[string]any) string {
	if s := text(m, "SpotStrategy"); s != "" && s != "NoSpot" {
		if s == "SpotWithPriceLimit" || s == "SpotAsPriceGo" {
			return "spot"
		}
		return "unknown"
	}
	v := text(m, "InstanceChargeType")
	if v == "" {
		v = text(m, "ChargeType")
	}
	switch v {
	case "PrePaid":
		return "subscription"
	case "PostPaid":
		return "pay_as_you_go"
	default:
		return "unknown"
	}
}
func baseInstance(a model.Account, kind, region string, r map[string]any, now time.Time) map[string]any {
	id := text(r, "InstanceId")
	name := text(r, "InstanceName")
	if name == "" {
		name = id
	}
	spec := text(r, "InstanceType")
	if kind == "swas" {
		spec = text(r, "PlanId")
	}
	var expires any
	if charge(r) == "subscription" {
		expires = utcTime(text(r, "ExpiredTime"))
	}
	return map[string]any{"id": resourceID(a, kind, region, id), "cloud_instance_id": id, "name": name, "account_id": a.ID, "kind": kind, "region_id": region, "zone_id": nullableText(text(r, "ZoneId")), "spec": spec, "status": status(text(r, "Status")), "public_ips": []map[string]any{}, "charge_type": charge(r), "expires_at": expires, "billing_subject_id": nil, "billing_subject_ids": []string{}, "billing_subject_kind": nil, "metering_rule": nil, "bandwidth_mbps": nil, "bandwidth_package_name": nil, "counts_toward_cdt": nil, "cdt_scope": nil, "agent": map[string]any{"status": "not_installed", "version": nil, "registered_at": nil, "last_report_at": nil, "offline_since": nil, "worst_case": nil}, "realtime_protection": "unprotected", "realtime": nil, "updated_at": now.Format(time.RFC3339)}
}
func subject(a model.Account, id, name, kind, rule, region string, bandwidth any, now time.Time) map[string]any {
	var eligible any
	var scope any
	if rule == "not_metered" || strings.HasPrefix(kind, "swas_") {
		eligible = false
	} else {
		scope = "overseas"
		if mainland(region) {
			scope = "mainland"
		}
	}
	return map[string]any{"id": id, "name": name, "account_id": a.ID, "kind": kind, "metering_rule": rule, "counts_toward_cdt": eligible, "cdt_scope": scope, "public_ips": []string{}, "instance_ids": []string{}, "bandwidth_mbps": bandwidth, "data_source": "none", "today_rx_bytes": nil, "today_tx_bytes": nil, "today_metered_bytes": nil, "updated_at": now.Format(time.RFC3339)}
}
func appendString(m map[string]any, key, value string) {
	if value == "" {
		return
	}
	list, _ := m[key].([]string)
	for _, v := range list {
		if v == value {
			return
		}
	}
	m[key] = append(list, value)
}
func addIP(i map[string]any, ip, kind string) {
	if ip == "" {
		return
	}
	list := i["public_ips"].([]map[string]any)
	for _, v := range list {
		if v["address"] == ip {
			return
		}
	}
	i["public_ips"] = append(list, map[string]any{"address": ip, "type": kind})
}

func normalizeRegion(a model.Account, region string, ecs, eni, eips, packages, swas collection, traffic map[string]bool, trafficKnown bool, now time.Time) model.ScanResult {
	out := model.ScanResult{Snapshots: []model.Snapshot{}, CompleteScopes: map[string]bool{}}
	instances := map[string]map[string]any{}
	subjects := map[string]map[string]any{}
	packs := map[string]map[string]any{}
	attachments := map[string]string{}
	for _, row := range eni.rows {
		id := text(row, "InstanceId")
		if id == "" {
			id = text(child(row, "Attachment"), "InstanceId")
		}
		attachments[text(row, "NetworkInterfaceId")] = id
	}
	bind := func(i, s map[string]any) {
		appendString(i, "billing_subject_ids", s["id"].(string))
		appendString(s, "instance_ids", i["id"].(string))
	}
	for _, r := range ecs.rows {
		i := baseInstance(a, "ecs", region, r, now)
		instances[text(r, "InstanceId")] = i
		if raw, ok := child(r, "PublicIpAddress")["IpAddress"].([]any); ok {
			for _, v := range raw {
				ip, _ := v.(string)
				addIP(i, ipAddress(ip), "instance_public_ip")
			}
		}
		if len(i["public_ips"].([]map[string]any)) > 0 {
			kind, rule := "unknown", "unknown"
			switch text(r, "InternetChargeType") {
			case "PayByTraffic":
				kind, rule = "ecs_pay_by_traffic", "outbound_only"
			case "PayByBandwidth":
				kind, rule = "ecs_fixed_bandwidth", "not_metered"
			}
			id := resourceID(a, "public", region, text(r, "InstanceId"))
			s := subject(a, id, i["name"].(string), kind, rule, region, number(r["InternetMaxBandwidthOut"]), now)
			for _, ip := range i["public_ips"].([]map[string]any) {
				appendString(s, "public_ips", ip["address"].(string))
			}
			subjects[id] = s
			bind(i, s)
		}
	}
	for _, r := range packages.rows {
		cloudID := text(r, "BandwidthPackageId")
		id := resourceID(a, "package", region, cloudID)
		name := text(r, "Name")
		if name == "" {
			name = cloudID
		}
		kind, rule := "unknown", "unknown"
		switch text(r, "InternetChargeType") {
		case "PayByDominantTraffic":
			kind, rule = "cbwp_dominant_traffic", "max_in_out_per_period"
		case "PayByBandwidth":
			kind, rule = "cbwp_bandwidth", "not_metered"
		case "PayBy95", "PayBy95Peak", "PayBy95Enhanced":
			kind, rule = "cbwp_95", "not_metered"
		}
		s := subject(a, id, name, kind, rule, region, number(r["Bandwidth"]), now)
		subjects[id] = s
		members := []map[string]any{}
		if rows, ok := objects(child(r, "PublicIpAddresses")["PublicIpAddresse"]); ok {
			for _, m := range rows {
				ip := ipAddress(text(m, "IpAddress"))
				if ip != "" {
					appendString(s, "public_ips", ip)
					members = append(members, map[string]any{"eip_address": ip, "instance_id": nil, "instance_name": nil})
				}
			}
		}
		packs[cloudID] = map[string]any{"id": id, "cloud_id": cloudID, "name": name, "account_id": a.ID, "region_id": region, "bandwidth_mbps": number(r["Bandwidth"]), "kind": kind, "settled_by_cdt": s["counts_toward_cdt"], "members": members, "today_rx_bytes": nil, "today_tx_bytes": nil, "today_metered_bytes": nil, "dominant_direction": nil, "updated_at": now.Format(time.RFC3339)}
	}
	seenEIP := map[string]bool{}
	for _, r := range eips.rows {
		cloudID := text(r, "AllocationId")
		seenEIP[cloudID] = true
		instanceID := text(r, "InstanceId")
		if linked := attachments[instanceID]; linked != "" {
			instanceID = linked
		}
		i := instances[instanceID]
		ip := ipAddress(text(r, "IpAddress"))
		if i != nil {
			addIP(i, ip, "eip")
		}
		packageID := text(r, "BandwidthPackageId")
		var s map[string]any
		if p := packs[packageID]; packageID != "" && p != nil {
			s = subjects[p["id"].(string)]
			members := p["members"].([]map[string]any)
			found := false
			for _, m := range members {
				if m["eip_address"] == ip {
					found = true
					if i != nil {
						m["instance_id"] = i["id"]
						m["instance_name"] = i["name"]
					}
				}
			}
			if !found && ip != "" {
				m := map[string]any{"eip_address": ip, "instance_id": nil, "instance_name": nil}
				if i != nil {
					m["instance_id"] = i["id"]
					m["instance_name"] = i["name"]
				}
				p["members"] = append(members, m)
			}
		} else {
			kind, rule := "unknown", "unknown"
			if packageID == "" && text(r, "InternetChargeType") == "PayByTraffic" {
				kind, rule = "eip_pay_by_traffic", "outbound_only"
			}
			if packageID == "" && text(r, "InternetChargeType") == "PayByBandwidth" {
				kind, rule = "eip_fixed_bandwidth", "not_metered"
			}
			id := resourceID(a, "eip", region, cloudID)
			s = subject(a, id, cloudID, kind, rule, region, number(r["Bandwidth"]), now)
			subjects[id] = s
		}
		appendString(s, "public_ips", ip)
		if i != nil {
			bind(i, s)
		}
	}
	// An ECS embedded EIP without a complete VPC lookup is visible but unresolved;
	// never misclassify a shared-bandwidth member as outbound-only traffic.
	for _, r := range ecs.rows {
		e := child(r, "EipAddress")
		id := text(e, "AllocationId")
		if id == "" || seenEIP[id] {
			continue
		}
		i := instances[text(r, "InstanceId")]
		ip := ipAddress(text(e, "IpAddress"))
		addIP(i, ip, "eip")
		sid := resourceID(a, "eip", region, id)
		s := subject(a, sid, id, "unknown", "unknown", region, number(e["Bandwidth"]), now)
		appendString(s, "public_ips", ip)
		subjects[sid] = s
		bind(i, s)
	}
	for _, r := range swas.rows {
		i := baseInstance(a, "swas", region, r, now)
		instances["swas:"+text(r, "InstanceId")] = i
		addIP(i, ipAddress(text(r, "PublicIpAddress")), "instance_public_ip")
		if rows, ok := objects(r["NetworkAttributes"]); ok {
			for _, m := range rows {
				addIP(i, ipAddress(text(m, "PublicIpAddress")), "instance_public_ip")
			}
		}
		kind, rule := "unknown", "unknown"
		if trafficKnown {
			kind, rule = "swas_no_traffic_package", "not_metered"
			if traffic[text(r, "InstanceId")] {
				kind, rule = "swas_traffic_package", "plan_defined"
			}
		}
		id := resourceID(a, "swas-plan", region, text(r, "InstanceId"))
		s := subject(a, id, i["name"].(string), kind, rule, region, number(child(r, "ResourceSpec")["Bandwidth"]), now)
		s["counts_toward_cdt"] = false
		s["cdt_scope"] = nil
		for _, ip := range i["public_ips"].([]map[string]any) {
			appendString(s, "public_ips", ip["address"].(string))
		}
		subjects[id] = s
		bind(i, s)
	}
	for _, i := range instances {
		ids := i["billing_subject_ids"].([]string)
		sort.Strings(ids)
		if len(ids) == 1 {
			s := subjects[ids[0]]
			i["billing_subject_id"] = s["id"]
			i["billing_subject_kind"] = s["kind"]
			i["metering_rule"] = s["metering_rule"]
			i["bandwidth_mbps"] = s["bandwidth_mbps"]
			i["counts_toward_cdt"] = s["counts_toward_cdt"]
			i["cdt_scope"] = s["cdt_scope"]
			if strings.HasPrefix(s["kind"].(string), "cbwp_") {
				i["bandwidth_package_name"] = s["name"]
			}
		}
		if i["kind"] == "swas" {
			i["counts_toward_cdt"] = false
			i["cdt_scope"] = nil
		}
		if i["kind"] == "ecs" && (eips.err != nil || eni.err != nil) {
			i["counts_toward_cdt"] = nil
			i["billing_subject_id"] = nil
			i["billing_subject_kind"] = "unknown"
			i["metering_rule"] = "unknown"
			i["bandwidth_mbps"] = nil
			i["bandwidth_package_name"] = nil
		}
		if i["counts_toward_cdt"] == false {
			i["realtime_protection"] = "not_applicable"
		}
		out.Snapshots = append(out.Snapshots, model.Snapshot{Kind: i["kind"].(string), ID: i["id"].(string), Region: region, Payload: i})
	}
	for id, s := range subjects {
		out.Snapshots = append(out.Snapshots, model.Snapshot{Kind: "subject", ID: id, Region: region, Payload: s})
	}
	for _, p := range packs {
		out.Snapshots = append(out.Snapshots, model.Snapshot{Kind: "package", ID: p["id"].(string), Region: region, Payload: p})
	}
	out.CompleteScopes[model.Scope("ecs", region)] = ecs.err == nil
	out.CompleteScopes[model.Scope("swas", region)] = swas.err == nil
	out.CompleteScopes[model.Scope("subject", region)] = ecs.err == nil && swas.err == nil && eips.err == nil && packages.err == nil && eni.err == nil && trafficKnown
	out.CompleteScopes[model.Scope("package", region)] = packages.err == nil
	return out
}
