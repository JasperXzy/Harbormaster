package store

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/JasperXzy/harbormaster/internal/ipinfo"
	"github.com/JasperXzy/harbormaster/internal/model"
	"github.com/JasperXzy/harbormaster/internal/netprobe"
	"github.com/JasperXzy/harbormaster/internal/security"
)

type snapshotFixture struct {
	t    *testing.T
	s    *Store
	plan model.ProbePlan
	spec model.TraceSchedule
	now  time.Time
}

func newSnapshotFixture(t *testing.T) *snapshotFixture {
	t.Helper()
	s := agentStore(t)
	now := time.Now().UTC().Truncate(24 * time.Hour).Add(-10*24*time.Hour + 2*time.Hour)
	// An encrypted, intentionally non-deliverable test channel exercises outbox
	// cancellation. No sender is started by these store tests.
	box, _ := security.NewBox(make([]byte, 32))
	cipher, _ := box.Encrypt([]byte("test-only-channel"), model.TelegramCredentialOwner)
	if _, err := s.SaveTelegramChannel(context.Background(), cipher, 0, model.AuditDetails{}, now); err != nil {
		t.Fatal(err)
	}
	target, err := s.SaveNetworkTarget(context.Background(), "", model.NetworkTarget{Name: "synthetic target", Kind: "custom", Host: new("trace.invalid"), TCPPort: new(443), Enabled: true}, now)
	if err != nil {
		t.Fatal(err)
	}
	plan := model.DefaultProbePlan()
	plan.Name = "trace snapshots"
	plan.PointKind = "server"
	plan.TargetID = target.ID
	plan.Family = "ipv4"
	plan.Method = "icmp"
	plan.Enabled = true
	plan, err = s.SaveProbePlan(context.Background(), "", plan, now)
	if err != nil {
		t.Fatal(err)
	}
	settings := model.DefaultTraceSchedule()
	settings.Enabled = true
	settings.NotifyChanges = true
	settings.Count = 1
	settings, err = s.SaveTraceSchedule(context.Background(), plan.ID, settings, "UTC", now)
	if err != nil {
		t.Fatal(err)
	}
	return &snapshotFixture{t: t, s: s, plan: plan, spec: settings, now: now}
}
func (f *snapshotFixture) runs() []model.TraceSnapshot {
	f.t.Helper()
	rows, err := f.s.TraceSnapshots(context.Background(), f.plan.ID, f.now.Add(-20*24*time.Hour), f.now.Add(time.Hour), 0, 201)
	if err != nil {
		f.t.Fatal(err)
	}
	return rows
}
func (f *snapshotFixture) due() model.TraceSnapshot {
	f.t.Helper()
	spec, err := f.s.TraceSchedule(context.Background(), f.plan.ID, "UTC")
	if err != nil || spec.NextRunAt == nil {
		f.t.Fatal(spec, err)
	}
	f.now = *spec.NextRunAt
	if err = f.s.ScheduleTraceSnapshots(context.Background(), "UTC", f.now); err != nil {
		f.t.Fatal(err)
	}
	return f.runs()[0]
}
func (f *snapshotFixture) finish(path []uint32, address, edition string, modify func(*model.TraceEnrichment)) model.TraceSnapshot {
	f.t.Helper()
	ctx := context.Background()
	c, err := f.s.ClaimServerTrace(ctx, f.now)
	if err != nil {
		f.t.Fatal(err)
	}
	r := netprobe.TraceResult{StartedAt: f.now, FinishedAt: f.now.Add(time.Second), Address: address, ResolvedAddresses: []string{address}, Hops: []netprobe.TraceHop{}}
	e := &model.TraceEnrichment{EnrichedAt: r.FinishedAt, Status: "complete", ASNDatabase: &ipinfo.Dataset{Name: "synthetic ASN", Edition: edition, BuiltAt: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), SizeBytes: 100}, Hops: []model.TraceHopInfo{}}
	for i, asn := range path {
		hopAddress := []string{"8.8.4.4", "9.9.9.9", "1.0.0.1", "208.67.222.222"}[i%4]
		reply, typ := "time_exceeded", 11
		if i == len(path)-1 {
			hopAddress = address
			reply = "echo_reply"
			typ = 0
		}
		h := netprobe.TraceHop{TTL: i + 1, Samples: []netprobe.TraceSample{}}
		for n := 0; n < c.Request.Count; n++ {
			h.Samples = append(h.Samples, netprobe.TraceSample{Sequence: i*c.Request.Count + n + 1, StartedAt: f.now, Address: new(hopAddress), RTTMS: new(float64(i + 1)), Reply: new(reply), ICMPType: new(typ), ICMPCode: new(0)})
		}
		r.Hops = append(r.Hops, h)
		e.Hops = append(e.Hops, model.TraceHopInfo{TTL: i + 1, Attempts: c.Request.Count, Responses: c.Request.Count, ASNCandidates: []uint32{asn}})
	}
	if err = f.s.FinishServerTrace(ctx, c, r, "", r.FinishedAt); err != nil {
		f.t.Fatal(err)
	}
	claim, err := f.s.ClaimTraceEnrichment(ctx, r.FinishedAt)
	if err != nil {
		f.t.Fatal(err)
	}
	if modify != nil {
		modify(e)
	}
	if err = f.s.FinishTraceEnrichment(ctx, claim, e, "", r.FinishedAt); err != nil {
		f.t.Fatal(err)
	}
	f.now = r.FinishedAt.Add(time.Second)
	if err = f.s.ScheduleTraceSnapshots(ctx, "UTC", f.now); err != nil {
		f.t.Fatal(err)
	}
	return f.runs()[0]
}

func TestTraceSnapshotsDailyConcurrentScheduleDowntimeAndRevisionFences(t *testing.T) {
	f := newSnapshotFixture(t)
	ctx := context.Background()
	if err := f.s.ScheduleTraceSnapshots(ctx, "UTC", f.now); err != nil {
		t.Fatal(err)
	}
	if len(f.runs()) != 0 {
		t.Fatal("ran before first daily point")
	}
	f.now = *f.spec.NextRunAt
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := f.s.ScheduleTraceSnapshots(ctx, "UTC", f.now); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	runs := f.runs()
	if len(runs) != 1 || runs[0].TraceID == nil || runs[0].Comparison.Status != "waiting_result" || !runs[0].DueAt.Equal(f.now) {
		t.Fatal(runs)
	}
	first := f.finish([]uint32{4134, 4134, 13335}, "1.1.1.1", "2026-11", nil)
	if first.Comparison.Status != "baseline" || !slices.Equal(first.Comparison.ASNs, []uint32{4134, 13335}) {
		t.Fatal(first)
	}
	f.now = f.now.Add(4 * 24 * time.Hour)
	if err := f.s.ScheduleTraceSnapshots(ctx, "UTC", f.now); err != nil {
		t.Fatal(err)
	}
	runs = f.runs()
	if len(runs) != 2 || !runs[0].CreatedAt.Equal(f.now) || !runs[0].DueAt.Before(runs[0].CreatedAt) {
		t.Fatal("backfilled missed dates", runs)
	}
	spec, err := f.s.TraceSchedule(ctx, f.plan.ID, "UTC")
	if err != nil || !spec.NextRunAt.After(f.now) || spec.NextRunAt.Sub(f.now) > 24*time.Hour {
		t.Fatal(spec, err)
	}
	jobID := *runs[0].JobID
	spec.Enabled = false
	if _, err = f.s.SaveTraceSchedule(ctx, f.plan.ID, spec, "UTC", f.now); err != nil {
		t.Fatal(err)
	}
	job, err := f.s.Job(ctx, jobID)
	if err != nil || job.Status != "failed" || job.Error.Code != "probe_configuration_changed" {
		t.Fatal(job, err)
	}
	if _, err = f.s.SaveTraceSchedule(ctx, f.plan.ID, spec, "UTC", f.now); !errors.Is(err, ErrNetworkChanged) {
		t.Fatal("stale revision accepted", err)
	}
}

func TestTraceSnapshotChangesAreSingleEventsAndDatabaseUpdatesDoNotAlert(t *testing.T) {
	f := newSnapshotFixture(t)
	f.due()
	f.finish([]uint32{4134, 4134, 13335}, "1.1.1.1", "2026-11", nil)
	f.due()
	changed := f.finish([]uint32{4134, 4809, 13335}, "1.1.1.1", "2026-11", nil)
	if changed.Comparison.Status != "changed" || changed.Comparison.NotificationID == nil {
		t.Fatal(changed)
	}
	for i := 0; i < 3; i++ {
		if err := f.s.ScheduleTraceSnapshots(context.Background(), "UTC", f.now); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err := f.s.DB.QueryRow("SELECT count(*) FROM notification WHERE rule_key='network_route_change'").Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
	f.due()
	same := f.finish([]uint32{4134, 4809, 13335}, "1.1.1.1", "2026-11", nil)
	if same.Comparison.Status != "unchanged" || same.Comparison.NotificationID != nil {
		t.Fatal(same)
	}
	f.due()
	updated := f.finish([]uint32{4134, 9929, 13335}, "1.1.1.1", "2026-12", nil)
	if updated.Comparison.Status != "not_comparable" || *updated.Comparison.Reason != "asn_database_changed" || updated.Comparison.NotificationID != nil {
		t.Fatal(updated)
	}
	f.due()
	after := f.finish([]uint32{4134, 9929, 13335}, "1.1.1.1", "2026-12", nil)
	if after.Comparison.Status != "unchanged" {
		t.Fatal("new version did not establish starting point", after)
	}
	if err := f.s.DeleteProbePlan(context.Background(), f.plan.ID, f.plan.Revision, f.now); err != nil {
		t.Fatal(err)
	}
	if len(f.runs()) != 5 {
		t.Fatal("retired plan lost history")
	}
}

func TestTraceSnapshotsUnknownMultipathTargetChangesAndConfigEditsCannotFire(t *testing.T) {
	for _, mode := range []string{"gap", "unknown", "multipath", "target", "config", "quiet", "global_off", "opt_out"} {
		t.Run(mode, func(t *testing.T) {
			f := newSnapshotFixture(t)
			f.due()
			f.finish([]uint32{4134, 13335}, "1.1.1.1", "2026-11", nil)
			if mode == "global_off" {
				if _, err := f.s.UpdateAlertRule(context.Background(), "network_route_change", model.AlertRuleUpdate{Enabled: new(false)}, f.now); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "config" || mode == "opt_out" {
				spec, err := f.s.TraceSchedule(context.Background(), f.plan.ID, "UTC")
				if err != nil {
					t.Fatal(err)
				}
				if mode == "opt_out" {
					spec.NotifyChanges = false
				} else {
					spec.MaxHops = 29
				}
				if _, err = f.s.SaveTraceSchedule(context.Background(), f.plan.ID, spec, "UTC", f.now); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "quiet" {
				if _, err := f.s.DB.Exec("INSERT INTO app_meta(key,value) VALUES('business_timezone','UTC')"); err != nil {
					t.Fatal(err)
				}
				q, err := f.s.QuietHours(context.Background(), f.now)
				if err != nil {
					t.Fatal(err)
				}
				q.Enabled, q.Start, q.End = true, "02:00", "04:00"
				if _, err = f.s.SaveQuietHours(context.Background(), q, f.now); err != nil {
					t.Fatal(err)
				}
			}
			f.due()
			address := "1.1.1.1"
			if mode == "target" {
				address = "8.8.8.8"
			}
			r := f.finish([]uint32{4809, 13335}, address, "2026-11", func(e *model.TraceEnrichment) {
				switch mode {
				case "gap":
					e.Hops[0].Responses = 0
				case "unknown":
					e.Hops[0].UnknownASN = true
				case "multipath":
					e.Hops[0].ASNCandidates = []uint32{4809, 4134}
				}
			})
			switch mode {
			case "quiet":
				if r.Comparison.Status != "changed" || r.Comparison.NotificationID == nil {
					t.Fatal(r)
				}
				n, err := f.s.Notification(context.Background(), *r.Comparison.NotificationID)
				if err != nil || n.Urgent || n.DeliveryStatus != "not_sent" || n.SuppressionReason == nil || *n.SuppressionReason != "daily" {
					t.Fatal(n, err)
				}
			case "global_off":
				if r.Comparison.Status != "changed" || r.Comparison.NotificationID != nil {
					t.Fatal(r)
				}
			default:
				if r.Comparison.Status != "not_comparable" || r.Comparison.NotificationID != nil {
					t.Fatal(r)
				}
			}
		})
	}
}

func TestTraceSnapshotQueueAndDailyAdvanceRollbackTogether(t *testing.T) {
	f := newSnapshotFixture(t)
	f.now = *f.spec.NextRunAt
	if _, err := f.s.DB.Exec("CREATE TRIGGER reject_snapshot BEFORE INSERT ON trace_snapshot_run BEGIN SELECT RAISE(ABORT,'test failure'); END"); err != nil {
		t.Fatal(err)
	}
	if err := f.s.ScheduleTraceSnapshots(context.Background(), "UTC", f.now); err == nil {
		t.Fatal("insertion failure ignored")
	}
	for _, table := range []string{"job", "network_trace_run", "trace_snapshot_run"} {
		var count int
		if err := f.s.DB.QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil || count != 0 {
			t.Fatal("partial schedule", table, count, err)
		}
	}
	spec, err := f.s.TraceSchedule(context.Background(), f.plan.ID, "UTC")
	if err != nil || !spec.NextRunAt.Equal(f.now) {
		t.Fatal("daily opportunity lost", spec, err)
	}
	if _, err = f.s.DB.Exec("DROP TRIGGER reject_snapshot"); err != nil {
		t.Fatal(err)
	}
	if err = f.s.ScheduleTraceSnapshots(context.Background(), "UTC", f.now); err != nil || len(f.runs()) != 1 {
		t.Fatal(err)
	}
}

func TestTraceSnapshotGapBreaksComparisonAndSettingsCancelPendingNotice(t *testing.T) {
	f := newSnapshotFixture(t)
	f.due()
	f.finish([]uint32{4134, 13335}, "1.1.1.1", "2026-11", nil)
	f.due()
	ctx := context.Background()
	// Let a queued snapshot expire, without inventing any measurement.
	f.now = f.now.Add(6 * time.Minute)
	if err := f.s.ScheduleTraceSnapshots(ctx, "UTC", f.now); err != nil {
		t.Fatal(err)
	}
	if r := f.runs()[0]; r.Status != "failed" || r.Comparison.Status != "not_comparable" {
		t.Fatal(r)
	}
	f.due()
	afterGap := f.finish([]uint32{4809, 13335}, "1.1.1.1", "2026-11", nil)
	if afterGap.Comparison.Status != "not_comparable" || *afterGap.Comparison.Reason != "previous_not_comparable" {
		t.Fatal("compared across missing observation", afterGap)
	}
	f.due()
	changed := f.finish([]uint32{9929, 13335}, "1.1.1.1", "2026-11", nil)
	if changed.Comparison.Status != "changed" || changed.Comparison.NotificationID == nil {
		t.Fatal(changed)
	}
	spec, err := f.s.TraceSchedule(ctx, f.plan.ID, "UTC")
	if err != nil {
		t.Fatal(err)
	}
	spec.Enabled = false
	if _, err = f.s.SaveTraceSchedule(ctx, f.plan.ID, spec, "UTC", f.now); err != nil {
		t.Fatal(err)
	}
	n, err := f.s.Notification(ctx, *changed.Comparison.NotificationID)
	if err != nil || n.DeliveryStatus != "not_sent" || n.LastError == nil || n.LastError.Code != "probe_configuration_changed" {
		t.Fatal("disabled schedule retained pending event", n, err)
	}
}

func TestTraceSnapshotsPlanPauseTimezoneAndRestoreDoNotReplayOldWork(t *testing.T) {
	f := newSnapshotFixture(t)
	f.due()
	ctx := context.Background()
	plan := f.plan
	plan.Enabled = false
	paused, err := f.s.SaveProbePlan(ctx, plan.ID, plan, f.now)
	if err != nil {
		t.Fatal(err)
	}
	view, err := f.s.TraceSchedule(ctx, plan.ID, "UTC")
	if err != nil || view.Active || view.NextRunAt != nil || !view.Enabled {
		t.Fatal(view, err)
	}
	f.now = f.now.Add(2 * 24 * time.Hour)
	if err = f.s.ScheduleTraceSnapshots(ctx, "UTC", f.now); err != nil {
		t.Fatal(err)
	}
	if len(f.runs()) != 1 {
		t.Fatal("paused plan scheduled")
	}
	paused.Enabled = true
	if _, err = f.s.SaveProbePlan(ctx, paused.ID, paused, f.now); err != nil {
		t.Fatal(err)
	}
	view, err = f.s.TraceSchedule(ctx, plan.ID, "UTC")
	if err != nil || view.NextRunAt == nil || !view.NextRunAt.After(f.now) {
		t.Fatal(view, err)
	}
	if _, err = f.s.DB.Exec("INSERT INTO app_meta(key,value) VALUES('business_timezone','Asia/Singapore')"); err != nil {
		t.Fatal(err)
	}
	if err = f.s.ScheduleTraceSnapshots(ctx, "UTC", f.now); err != nil {
		t.Fatal(err)
	}
	view, err = f.s.TraceSchedule(ctx, plan.ID, "UTC")
	if err != nil || view.Timezone != "Asia/Singapore" || !view.NextRunAt.After(f.now) || view.NextRunAt.In(time.FixedZone("SG", 8*3600)).Hour() != 3 {
		t.Fatal(view, err)
	}
	if _, err = f.s.PrepareRestoredCopy(ctx, f.now); err != nil {
		t.Fatal(err)
	}
	rows := f.runs()
	if rows[0].Comparison.Reason == nil || *rows[0].Comparison.Reason != "measurement_failed" {
		t.Fatal(rows)
	}
	// A separate pending comparison is cancelled in a backup before evaluation.
	f.now = *view.NextRunAt
	if err = f.s.ScheduleTraceSnapshots(ctx, "Asia/Singapore", f.now); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.PrepareRestoredCopy(ctx, f.now); err != nil {
		t.Fatal(err)
	}
	rows = f.runs()
	if rows[0].Comparison.Status != "not_comparable" || *rows[0].Comparison.Reason != "backup_restored" {
		t.Fatal(rows)
	}
	var owner string
	if err = f.s.DB.QueryRow("SELECT coalesce(lease_owner,'') FROM job WHERE id=?", *rows[0].JobID).Scan(&owner); err != nil || owner != "" {
		t.Fatal(owner, err)
	}
	// Stored list projection remains independent of packet/enrichment bodies.
	if _, err = f.s.DB.Exec("UPDATE network_probe_job SET result_json='invalid json' WHERE job_id=?", *rows[0].JobID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.DB.Exec("UPDATE trace_enrichment SET state='failed',error_code='test',result_json=NULL WHERE trace_seq=?", *rows[0].TraceID); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(f.runs())
	if len(data) == 0 {
		t.Fatal("empty projection")
	}
	before, _ := strconv.ParseInt(rows[0].ID, 10, 64)
	older, err := f.s.TraceSnapshots(ctx, plan.ID, f.now.Add(-10*24*time.Hour), f.now.Add(time.Hour), before, 1)
	if err != nil || len(older) != 1 || older[0].ID == rows[0].ID {
		t.Fatal(older, err)
	}
}
