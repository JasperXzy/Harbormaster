package devmode

import (
	"net/http"
	"strings"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
)

const demoDuplexID = "demo-duplex"

func demoDuplexLegs(now time.Time) []model.TraceDetail {
	out := []model.TraceDetail{}
	for i, direction := range []string{"forward", "reverse"} {
		// Each base fixture is freshly allocated, preserving independent samples.
		d := demoTraceFixtures(now)[0]
		d.ID, d.JobID = "3", "demo-duplex-job-forward"
		d.PointID, d.PointName = "demo-sin", "sin-pro (demo)"
		address, port := "1.1.1.1", 443
		if i == 1 {
			d.ID, d.JobID = "4", "demo-duplex-job-reverse"
			d.PointID, d.PointName = "demo-hkg", "hkg-pro (demo)"
			address, port = "8.8.8.8", 8443
		}
		d.PointKind, d.InstanceID, d.Target = "agent", &d.PointID, nil
		d.DuplexID, d.DuplexDirection = new(demoDuplexID), &direction
		d.Request.Host, d.Request.Port = address, port
		r := d.Result
		shift := time.Duration(i*3) * time.Second
		r.StartedAt, r.FinishedAt = r.StartedAt.Add(shift), r.FinishedAt.Add(shift)
		for h := range r.Hops {
			for n := range r.Hops[h].Samples {
				s := &r.Hops[h].Samples[n]
				s.StartedAt = s.StartedAt.Add(shift)
				if s.Address != nil && *s.Address == r.Address {
					s.Address = &address
				}
			}
		}
		r.Address, r.ResolvedAddresses = address, []string{address}
		summary := model.SummarizeTrace(*r)
		d.Outcome, d.UpdatedAt = &summary, r.FinishedAt
		d.Enrichment = demoTraceInfo(*r)
		out = append(out, d)
	}
	return out
}
func demoDuplexTrace(now time.Time) model.DuplexTrace {
	legs := demoDuplexLegs(now)
	at := legs[0].CreatedAt
	return model.DuplexTrace{ID: demoDuplexID, CreatedAt: at, From: model.DuplexTraceEndpoint{InstanceID: "demo-sin", Name: "sin-pro (demo)", Kind: "ecs", PublicAddress: "8.8.8.8", ObservedAt: at.Add(-time.Minute)}, To: model.DuplexTraceEndpoint{InstanceID: "demo-hkg", Name: "hkg-pro (demo)", Kind: "ecs", PublicAddress: "1.1.1.1", ObservedAt: at.Add(-time.Minute)}, Forward: legs[0], Reverse: legs[1]}
}
func demoDuplexTraces(w http.ResponseWriter, r *http.Request, path string, now time.Time) bool {
	if path != "/network/duplex-traces" && !strings.HasPrefix(path, "/network/duplex-traces/") {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(path, "/network/duplex-traces/"), "/")
	if path == "/network/duplex-traces" {
		failure(w, 405, "method_not_allowed")
		return true
	}
	if len(parts) > 2 || parts[0] != demoDuplexID || len(parts) == 2 && parts[1] != "export" {
		failure(w, 404, "not_found")
		return true
	}
	d := demoDuplexTrace(now)
	if len(parts) == 1 {
		respond(w, 200, d)
	} else {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="duplex-trace-`+d.ID+`.txt"`)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		_, _ = w.Write([]byte(model.DuplexTraceText(d)))
	}
	return true
}
