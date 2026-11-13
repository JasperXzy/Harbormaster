package api

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
	"github.com/JasperXzy/harbormaster/internal/netprobe"
)

func TestProbeRetentionReadModelsNeverPruneOnGET(t *testing.T) {
	p := testPanel(t)
	now := time.Now().UTC()
	p.now = func() time.Time { return now }
	cookie, _ := setupPanel(t, p)
	ctx := context.Background()
	at := now.Add(-31 * 24 * time.Hour)
	plan := model.DefaultProbePlan()
	plan.Name = "retained history"
	plan.PointKind = "server"
	plan.TargetID = "target_cloudflare_dns"
	plan.Family = "ipv4"
	plan.Method = "tcp"
	plan.Count = 1
	plan.Enabled = true
	plan, err := p.s.SaveProbePlan(ctx, "", plan, at)
	if err != nil {
		t.Fatal(err)
	}
	if err = p.s.ScheduleProbePlans(ctx, at); err != nil {
		t.Fatal(err)
	}
	claim, err := p.s.ClaimServerProbe(ctx, at)
	if err != nil {
		t.Fatal(err)
	}
	result := netprobe.Result{StartedAt: at, FinishedAt: at.Add(time.Second), Address: "1.1.1.1", ResolvedAddresses: []string{"1.1.1.1"}, Samples: []netprobe.Sample{{Sequence: 1, StartedAt: at, RTTMS: new(float64(7))}}}
	if err = p.s.FinishServerProbe(ctx, claim, result, "", at.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	path := "/network/plans/" + plan.ID + "/runs?" + url.Values{"from": {at.Add(-time.Minute).Format(time.RFC3339Nano)}, "to": {at.Add(time.Minute).Format(time.RFC3339Nano)}}.Encode()
	for _, target := range []string{"/settings", path, "/jobs/" + claim.Job.ID} {
		if w := panelRequest(p, "GET", target, nil, cookie, ""); w.Code != 200 {
			t.Fatal(w.Code, w.Body)
		}
	}
	settings := panelRequest(p, "GET", "/settings", nil, cookie, "")
	if !strings.Contains(settings.Body.String(), `"latency_probe_days":30`) {
		t.Fatal(settings.Body)
	}
	var count int
	if err = p.s.DB.QueryRow("SELECT count(*) FROM network_probe_job WHERE result_json IS NOT NULL AND result_pruned_at IS NULL").Scan(&count); err != nil || count != 1 {
		t.Fatal("GET pruned expired measurements", count, err)
	}
	if _, err = p.s.PruneProbeHistory(ctx, now, 30, 10); err != nil {
		t.Fatal(err)
	}
	w := panelRequest(p, "GET", "/jobs/"+claim.Job.ID, nil, cookie, "")
	var job model.Job
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &job) != nil || job.Status != "succeeded" || !strings.Contains(w.Body.String(), `"result_expired":true`) || strings.Contains(w.Body.String(), "rtt_ms") {
		t.Fatal(w.Code, w.Body)
	}
	w = panelRequest(p, "GET", path, nil, cookie, "")
	var runs struct {
		Items []model.ProbeRun `json:"items"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &runs) != nil || runs.Items == nil || len(runs.Items) != 0 {
		t.Fatal("expired history invented data", w.Code, w.Body)
	}
	if w = panelRequest(p, "GET", "/jobs/"+claim.Job.ID, nil, nil, ""); w.Code != 401 {
		t.Fatal("expiry metadata lost authentication", w.Code)
	}
}
