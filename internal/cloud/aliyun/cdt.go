package aliyun

import (
	"context"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
)

func (c Inventory) Traffic(ctx context.Context, a model.Account, credential model.Credential, before func() error) model.CDTObservation {
	if err := before(); err != nil {
		return model.CDTObservation{AccountID: a.ID, FetchedAt: time.Now().UTC(), Quality: "unknown", Error: model.Error("account_changed")}
	}
	body, err := c.Caller.Call(ctx, Credentials{Credential: credential, Site: a.Site, Regions: a.RegionIDs}, "ListCdtInternetTraffic", "cn-hongkong", map[string]string{"RegionId": "cn-hongkong"})
	now := time.Now().UTC()
	if err != nil {
		return model.CDTObservation{AccountID: a.ID, FetchedAt: now, Quality: "unknown", Error: model.Error(Code(err))}
	}
	return parseCDT(a.ID, body, now)
}

func parseCDT(account string, body map[string]any, now time.Time) model.CDTObservation {
	o := model.CDTObservation{AccountID: account, FetchedAt: now, Quality: "inferred", Details: []model.CDTRegion{}}
	bad := func(code string) model.CDTObservation {
		o.Mainland = nil
		o.Overseas = nil
		o.Quality = "unknown"
		o.Error = model.Error(code)
		return o
	}
	v, ok := body["TrafficDetails"]
	if !ok {
		v, ok = valueAt(body, "Data.TrafficDetails")
	}
	rows, valid := objects(v)
	if !ok || !valid {
		return bad("cdt_response_unrecognized")
	}
	seen := map[string]bool{}
	for _, r := range rows {
		region, isp := text(r, "BusinessRegionId"), text(r, "ISPType")
		amount, ok := integer(r["Traffic"])
		if !ok || amount > 1<<53 || !model.RegionPattern.MatchString(region) || isp == "" || seen[region+":"+isp] {
			return bad("cdt_response_unrecognized")
		}
		seen[region+":"+isp] = true
		o.Details = append(o.Details, model.CDTRegion{Region: region, ISP: isp, ReportedTraffic: amount})
		// Do not silently mix premium/unknown lines into the BGP quota pool.
		if isp != "bgp" {
			return bad("cdt_line_unverified")
		}
		target := &o.Overseas
		if mainland(region) {
			target = &o.Mainland
		}
		if *target == nil {
			zero := int64(0)
			*target = &zero
		}
		if **target > (1<<53)-amount {
			return bad("cdt_value_out_of_range")
		}
		**target += amount
	}
	// No rows for a scope is unknown; an explicit Traffic:0 remains a known zero.
	return o
}
