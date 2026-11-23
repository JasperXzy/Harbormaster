package devmode

import (
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
)

func demoIdle(w http.ResponseWriter, r *http.Request, path string, f fixtures, now time.Time) bool {
	if path != "/idle-resources" {
		return false
	}
	q := r.URL.Query()
	limit := 50
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 200 {
			failure(w, 422, "validation_failed")
			return true
		}
		limit = n
	}
	filter := model.IdleFilter{AccountID: q.Get("account_id"), RegionID: q.Get("region_id"), Kind: q.Get("kind"), Limit: limit + 1}
	if !filter.Valid() {
		failure(w, 422, "validation_failed")
		return true
	}
	if raw := q.Get("cursor"); raw != "" {
		after, ok := model.DecodeIdleCursor(raw, filter)
		if !ok {
			failure(w, 422, "validation_failed")
			return true
		}
		filter.After = after
	}
	at := now.Add(-time.Minute)
	ip, size := "192.0.2.61", int64(40)
	candidates := []model.IdleCandidate{
		{ID: "demo-account-1_eip_ap-southeast-1_eip-demo-unused", AccountID: "demo-account-1", RegionID: "ap-southeast-1", Kind: "eip", CloudID: "eip-demo-unused", Name: "Unbound EIP (demo)", Reason: "unbound_eip", ChargeType: "pay_as_you_go", CloudStatus: "Available", InstanceIDs: []string{}, PublicIP: &ip, ObservedAt: at, SourceStatus: "ok", EvidenceStatus: "current"},
		{ID: "demo-account-1_disk_cn-hongkong_d-demo-unused", AccountID: "demo-account-1", RegionID: "cn-hongkong", Kind: "disk", CloudID: "d-demo-unused", Name: "Unattached disk (demo)", Reason: "unattached_cloud_disk", ChargeType: "pay_as_you_go", CloudStatus: "Available", InstanceIDs: []string{}, SizeGiB: &size, ObservedAt: at, SourceStatus: "ok", EvidenceStatus: "current"},
	}
	sources := []model.IdleSource{}
	for _, account := range f.Accounts {
		id := account["id"].(string)
		for _, region := range account["region_ids"].([]string) {
			for _, kind := range model.IdleResourceKinds {
				if (filter.AccountID != "" && filter.AccountID != id) || (filter.RegionID != "" && filter.RegionID != region) || (filter.Kind != "" && filter.Kind != kind) {
					continue
				}
				scanned, unknown, count := 0, 0, 0
				for _, instance := range f.Instances {
					if instance["account_id"] != id || instance["region_id"] != region {
						continue
					}
					if instance["kind"] == kind || (kind == "disk" && instance["kind"] == "ecs") {
						scanned++
					}
					if kind == "eip" {
						for _, ip := range instance["public_ips"].([]object) {
							if ip["type"] == "eip" {
								scanned++
							}
						}
					}
				}
				for _, candidate := range candidates {
					if candidate.AccountID == id && candidate.RegionID == region && candidate.Kind == kind {
						count++
						scanned++
					}
				}
				v := model.IdleSource{AccountID: id, RegionID: region, Kind: kind, Source: model.IdleSourceName(kind), Enabled: true, FetchedAt: &at, LastAttemptAt: &at, Scanned: &scanned, Unclassified: &unknown, Candidates: &count}
				v.Age(now)
				sources = append(sources, v)
			}
		}
	}
	items := []model.IdleCandidate{}
	for _, v := range candidates {
		if (filter.AccountID == "" || filter.AccountID == v.AccountID) && (filter.RegionID == "" || filter.RegionID == v.RegionID) && (filter.Kind == "" || filter.Kind == v.Kind) && (filter.After == "" || v.ID > filter.After) {
			items = append(items, v)
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	sort.Slice(sources, func(i, j int) bool {
		a, b := sources[i], sources[j]
		if a.AccountID != b.AccountID {
			return a.AccountID < b.AccountID
		}
		if a.RegionID != b.RegionID {
			return a.RegionID < b.RegionID
		}
		return a.Kind < b.Kind
	})
	var next any
	if len(items) > limit {
		next = model.EncodeIdleCursor(items[limit-1].ID, filter)
		items = items[:limit]
	}
	respond(w, 200, object{"items": items, "sources": sources, "next_cursor": next})
	return true
}
