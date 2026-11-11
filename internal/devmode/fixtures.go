// Package devmode implements synthetic M0 read APIs from the M1 draft. It never
// constructs a cloud or notification client and is mounted only on loopback.
package devmode

import (
	"time"
)

type object = map[string]any

type fixtures struct {
	Accounts      []object
	Instances     []object
	Quotas        []object
	Subjects      []object
	Packages      []object
	Notifications []object
	Rules         []object
}

func makeFixtures(now time.Time, loc *time.Location) fixtures {
	f := fixtures{}
	stamp := now.UTC().Format(time.RFC3339)
	official := now.Add(-time.Hour).UTC().Format(time.RFC3339)
	local := now.In(loc)
	start := time.Date(local.Year(), local.Month(), 1, 0, 0, 0, 0, loc)
	end := start.AddDate(0, 1, 0)
	for i, id := range []string{"demo-account-1", "demo-account-2"} {
		f.Accounts = append(f.Accounts, object{"id": id, "name": []string{"Example account A (demo)", "Example account B (demo)"}[i], "site": []string{"china", "international"}[i], "access_key_id_masked": "DEMO***ONLY", "region_ids": []string{"ap-southeast-1", "cn-hongkong", "cn-shanghai"}, "enabled": true, "quota_mainland_bytes": int64(20 << 30), "quota_overseas_bytes": int64(200 << 30), "reset_day": 1, "alert_thresholds_percent": []int{80, 90, 95}, "sync_status": "ok", "last_sync_at": stamp, "last_error": nil, "consecutive_failures": 0, "permission_check": nil, "instance_count": []int{2, 1}[i], "updated_at": stamp})
		for j, scope := range []string{"mainland", "overseas"} {
			quota := int64(20 << 30)
			if j == 1 {
				quota = 200 << 30
			}
			used := quota / 4
			inc := int64(128 << 20)
			f.Quotas = append(f.Quotas, object{"account_id": id, "scope": scope, "cycle_start": start.UTC().Format(time.RFC3339), "cycle_end": end.UTC().Format(time.RFC3339), "quota_bytes": quota, "official_bytes": used, "official_as_of": official, "official_status": "ok", "realtime_increment_bytes": inc, "worst_case_bytes": 0, "estimated_bytes": used + inc, "effective_bytes": used + inc, "thresholds_percent": []int{80, 90, 95}, "forecast": nil, "updated_at": stamp})
		}
	}
	for i, id := range []string{"demo-sin", "demo-hkg", "demo-sha"} {
		account := "demo-account-1"
		kind := "ecs"
		rule := "outbound_only"
		billing := "ecs_pay_by_traffic"
		var scope any = "overseas"
		var packageName any
		counts := true
		agent := object{"status": "not_installed", "version": nil, "registered_at": nil, "last_report_at": nil, "offline_since": nil, "worst_case": nil}
		if i <= 1 {
			billing = "cbwp_dominant_traffic"
			rule = "max_in_out_per_period"
			packageName = []string{"sg-share (demo)", "hk-share (demo)"}[i]
		}
		if i == 0 {
			agent = object{"status": "online", "version": "v0.1.0", "available_version": "v0.2.0", "update_available": true, "registered_at": stamp, "last_report_at": stamp, "offline_since": nil, "worst_case": nil}
		}
		protection := "unprotected"
		if i == 0 {
			protection = "protected"
		}
		if i == 2 {
			account = "demo-account-2"
			kind = "swas"
			billing = "swas_no_traffic_package"
			rule = "not_metered"
			scope = nil
			counts = false
			protection = "not_applicable"
		}
		var realtime any
		if i == 0 {
			realtime = object{"rx_bytes_per_second": 100000, "tx_bytes_per_second": 80000, "sampled_at": stamp}
		}
		name := []string{"sin-pro (demo)", "hkg-pro (demo)", "sha-pro (demo)"}[i]
		ip := []string{"192.0.2.10", "192.0.2.20", "198.51.100.30"}[i]
		ipType := "instance_public_ip"
		if i <= 1 {
			ipType = "eip"
		}
		var capacity any
		if kind == "ecs" {
			capacity = object{"source": "ecs_describe_instance_types", "checked_at": stamp, "rx_kbps": 1000000, "tx_kbps": 1000000, "public_outbound_mbps": 2000, "public_outbound_exceeds_spec": true, "last_error": nil}
		}
		f.Instances = append(f.Instances, object{"id": id, "cloud_instance_id": []string{"i-demo-sin", "i-demo-hkg", "swas-demo-sha"}[i], "name": name, "account_id": account, "kind": kind, "region_id": []string{"ap-southeast-1", "cn-hongkong", "cn-shanghai"}[i], "zone_id": nil, "spec": "example-spec", "network_capacity": capacity, "status": "running", "public_ips": []object{{"address": ip, "type": ipType}}, "charge_type": []string{"pay_as_you_go", "pay_as_you_go", "subscription"}[i], "expires_at": nil, "billing_subject_id": "subject-" + id, "billing_subject_kind": billing, "metering_rule": rule, "bandwidth_mbps": []int{2000, 2000, 200}[i], "bandwidth_package_name": packageName, "counts_toward_cdt": counts, "cdt_scope": scope, "agent": agent, "realtime_protection": protection, "realtime": realtime, "updated_at": stamp})
		var rx, tx, metered any
		source := "none"
		if i == 0 {
			rx = int64(3 << 30)
			tx = int64(2 << 30)
			metered = rx
			source = "agent"
		}
		f.Subjects = append(f.Subjects, object{"id": "subject-" + id, "name": name, "account_id": account, "kind": billing, "metering_rule": rule, "counts_toward_cdt": counts, "cdt_scope": scope, "public_ips": []string{ip}, "instance_ids": []string{id}, "bandwidth_mbps": []int{2000, 2000, 200}[i], "data_source": source, "today_rx_bytes": rx, "today_tx_bytes": tx, "today_metered_bytes": metered, "updated_at": stamp})
	}
	f.Packages = []object{{"id": "demo-package", "cloud_id": "cbwp-demo-only", "name": "sg-share (demo)", "account_id": "demo-account-1", "region_id": "ap-southeast-1", "bandwidth_mbps": 2000, "kind": "cbwp_dominant_traffic", "settled_by_cdt": true, "members": []object{{"eip_address": "192.0.2.10", "instance_id": "demo-sin", "instance_name": "sin-pro (demo)"}}, "today_rx_bytes": int64(3 << 30), "today_tx_bytes": int64(2 << 30), "today_metered_bytes": int64(3 << 30), "dominant_direction": "inbound", "updated_at": stamp}}
	f.Packages = append(f.Packages, object{"id": "demo-package-hk", "cloud_id": "cbwp-hk-demo-only", "name": "hk-share (demo)", "account_id": "demo-account-1", "region_id": "cn-hongkong", "bandwidth_mbps": 2000, "kind": "cbwp_dominant_traffic", "settled_by_cdt": true, "members": []object{{"eip_address": "192.0.2.20", "instance_id": "demo-hkg", "instance_name": "hkg-pro (demo)"}}, "today_rx_bytes": nil, "today_tx_bytes": nil, "today_metered_bytes": nil, "dominant_direction": nil, "updated_at": nil})
	f.Notifications = []object{{"id": "demo-notification", "created_at": stamp, "kind": "test", "severity": "info", "rule_key": nil, "title": "M0 demo placeholder", "body": "Synthetic data; no notification was sent.", "account_id": nil, "instance_id": nil, "delivery_status": "not_sent"}}
	for _, key := range []string{"cdt_usage", "cdt_forecast_exceed", "account_api_failure", "agent_offline", "cdt_official_unavailable"} {
		params := object{}
		switch key {
		case "cdt_usage":
			params["thresholds_percent"] = []int{80, 90, 95}
		case "agent_offline":
			params["offline_after_seconds"] = 30
		case "account_api_failure", "cdt_official_unavailable":
			params["consecutive_failures"] = 3
		}
		f.Rules = append(f.Rules, object{"key": key, "enabled": true, "params": params, "urgent": key != "account_api_failure"})
	}
	return f
}
