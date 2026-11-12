package devmode

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/JasperXzy/harbormaster/internal/ipinfo"
	"github.com/JasperXzy/harbormaster/internal/model"
	"github.com/JasperXzy/harbormaster/internal/netprobe"
)

func demoTraceFixtures(now time.Time) []model.TraceDetail {
	out := []model.TraceDetail{}
	for i := 1; i <= 2; i++ {
		id := "1"
		if i == 2 {
			id = "2"
		}
		at := now.Truncate(time.Minute).Add(-time.Duration((3-i)*5) * time.Minute)
		q := netprobe.TraceRequest{Host: "1.1.1.1", Family: "ipv4", Method: "tcp", Port: 443, MaxHops: 3, Count: 3, TimeoutMS: 1000}
		r := netprobe.TraceResult{StartedAt: at, FinishedAt: at.Add(10 * time.Second), Address: q.Host, ResolvedAddresses: []string{q.Host}, Hops: []netprobe.TraceHop{}, StopReason: "max_hops"}
		for ttl := 1; ttl <= 3; ttl++ {
			h := netprobe.TraceHop{TTL: ttl, Samples: []netprobe.TraceSample{}}
			for n := 1; n <= 3; n++ {
				seq := (ttl-1)*3 + n
				s := netprobe.TraceSample{Sequence: seq, StartedAt: at.Add(time.Duration(seq-1) * time.Second)}
				if ttl == 2 && n == 2 {
					s.ErrorCode = new("probe_timeout")
				} else {
					s.Address = new("10.0.0.1")
					s.RTTMS = new(float64(ttl*8 + n))
					s.Reply = new("time_exceeded")
					s.ICMPType = new(11)
					s.ICMPCode = new(0)
					if ttl == 2 {
						s.Address = new("192.0.2.1")
					}
					if ttl == 3 && i == 1 {
						s.Address = new(q.Host)
						s.Reply = new("tcp_reset")
						s.ICMPType = nil
						s.ICMPCode = nil
						r.Reached = true
						r.StopReason = "reached"
					}
				}
				h.Samples = append(h.Samples, s)
			}
			r.Hops = append(r.Hops, h)
		}
		v := model.TraceRun{ID: id, JobID: "demo-trace-job-" + id, PointID: "server", PointName: "hm-server (demo)", PointKind: "server", Request: q, CreatedAt: at, UpdatedAt: r.FinishedAt, Status: "succeeded"}
		if i == 1 {
			v.Target = &model.NetworkTarget{ID: "target_cloudflare_dns", Revision: 1, Name: "1.1.1.1", Kind: "custom", Host: new(q.Host), TCPPort: new(443), Enabled: true, CreatedAt: at.Add(-time.Hour), UpdatedAt: at.Add(-time.Hour)}
		} else {
			v.PointID = "demo-sin"
			v.PointName = "sin-pro (demo)"
			v.PointKind = "agent"
			v.InstanceID = new(v.PointID)
		}
		summary := model.SummarizeTrace(r)
		v.Outcome = &summary
		v.EnrichmentStatus = "complete"
		out = append(out, model.TraceDetail{TraceRun: v, Result: &r, Enrichment: demoTraceInfo(r)})
	}
	return out
}

// Explicit synthetic fixtures: demo reads never resolve PTR or open databases.
func demoTraceInfo(r netprobe.TraceResult) *model.TraceEnrichment {
	dataset := ipinfo.Dataset{Name: "synthetic ASN (demo)", Edition: "2026-11", BuiltAt: r.StartedAt, Origin: "bundled", Storage: "memory", SourceURL: "https://db-ip.com"}
	geo := dataset
	geo.Name = "synthetic country (demo)"
	e := &model.TraceEnrichment{EnrichedAt: r.FinishedAt, Status: "complete", ASNDatabase: &dataset, LocationDatabase: &geo, Addresses: []model.TraceAddressInfo{}, Hops: []model.TraceHopInfo{}, Labels: []model.TraceNetworkLabel{}, DetourStatus: "unconfirmed", Attribution: ipinfo.AttributionNotice()}
	seen := map[string]bool{}
	for _, h := range r.Hops {
		info := model.TraceHopInfo{TTL: h.TTL, Attempts: 3, Responses: 3, NoResponsePercent: new(float64(0)), MinimumMS: new(float64(h.TTL*8 + 1)), MaximumMS: new(float64(h.TTL*8 + 3)), AverageMS: new(float64(h.TTL*8 + 2)), Addresses: []string{}, ASNCandidates: []uint32{}, CountryCodes: []string{}, UnknownASN: true, UnknownLocation: true}
		if h.TTL == 2 {
			info.Responses, info.NoResponsePercent = 2, new(float64(100)/3)
		}
		for _, s := range h.Samples {
			if s.Address == nil {
				continue
			}
			address := *s.Address
			if len(info.Addresses) == 0 {
				info.Addresses = append(info.Addresses, address)
			}
			if address == "1.1.1.1" || address == "8.8.8.8" {
				asn, country := uint32(13335), "AU"
				if address == "8.8.8.8" {
					asn, country = 15169, "US"
				}
				info.ASNCandidates, info.CountryCodes = []uint32{asn}, []string{country}
				info.UnknownASN, info.UnknownLocation = false, false
			}
			if seen[address] {
				continue
			}
			seen[address] = true
			a := model.TraceAddressInfo{Record: ipinfo.Record{Address: address, ASNStatus: "not_public", LocationStatus: "not_public"}, PTR: model.TracePTR{Status: "not_public", Names: []string{}}}
			if address == "1.1.1.1" {
				a.ASNStatus, a.LocationStatus = "ok", "ok"
				a.ASN = &ipinfo.ASN{Number: 13335, Organization: new("Cloudflare (demo)"), Prefix: "1.1.1.0/24", Dataset: dataset}
				a.Location = &ipinfo.Location{CountryCode: new("AU"), CountryName: new("Australia (demo)"), Prefix: "1.1.1.0/24", Granularity: "country", Dataset: geo}
				a.PTR = model.TracePTR{Status: "ok", Names: []string{"router.example"}}
			}
			if address == "8.8.8.8" {
				a.ASNStatus, a.LocationStatus = "ok", "ok"
				a.ASN = &ipinfo.ASN{Number: 15169, Organization: new("Google (demo)"), Prefix: "8.8.8.0/24", Dataset: dataset}
				a.Location = &ipinfo.Location{CountryCode: new("US"), CountryName: new("United States (demo)"), Prefix: "8.8.8.0/24", Granularity: "country", Dataset: geo}
				a.PTR = model.TracePTR{Status: "ok", Names: []string{"reverse.example"}}
			}
			e.Addresses = append(e.Addresses, a)
		}
		e.Hops = append(e.Hops, info)
	}
	return e
}
func demoTraces(w http.ResponseWriter, r *http.Request, path string, now time.Time) bool {
	if path != "/network/traces" && !strings.HasPrefix(path, "/network/traces/") {
		return false
	}
	fixtures := append(demoTraceFixtures(now), demoDuplexLegs(now)...)
	if path == "/network/traces" {
		q := r.URL.Query()
		from, e1 := time.Parse(time.RFC3339Nano, q.Get("from"))
		to, e2 := time.Parse(time.RFC3339Nano, q.Get("to"))
		f := model.TraceFilter{From: from, To: to, PointID: q.Get("point_id"), TargetID: q.Get("target_id"), Limit: 50}
		if e1 != nil || e2 != nil || !f.Validate() || to.After(now) {
			failure(w, 422, "validation_failed")
			return true
		}
		items := []model.TraceRun{}
		for _, v := range fixtures {
			if v.CreatedAt.Before(from) || !v.CreatedAt.Before(to) || f.PointID != "" && f.PointID != v.PointID || f.TargetID != "" && (v.Target == nil || v.Target.ID != f.TargetID) {
				continue
			}
			items = append(items, v.TraceRun)
		}
		filters, _ := json.Marshal([]string{f.PointID, f.TargetID})
		demoProbePage(w, r, items, demoProbeCursor{Kind: "traces", ID: string(filters), From: from.UTC().Format(time.RFC3339Nano), To: to.UTC().Format(time.RFC3339Nano)}, func(v model.TraceRun) string { return v.ID }, true)
		return true
	}
	parts := strings.Split(strings.TrimPrefix(path, "/network/traces/"), "/")
	if len(parts) > 2 || len(parts) == 2 && parts[1] != "export" {
		failure(w, 404, "not_found")
		return true
	}
	for _, v := range fixtures {
		if v.ID == parts[0] {
			if len(parts) == 1 {
				respond(w, 200, v)
			} else {
				w.Header().Set("Content-Type", "text/plain; charset=utf-8")
				w.Header().Set("Content-Disposition", `attachment; filename="trace-`+v.ID+`.txt"`)
				w.Header().Set("X-Content-Type-Options", "nosniff")
				_, _ = w.Write([]byte(model.TraceText(v)))
			}
			return true
		}
	}
	failure(w, 404, "not_found")
	return true
}
