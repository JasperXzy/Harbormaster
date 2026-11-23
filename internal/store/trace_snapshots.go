package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"time"

	"github.com/JasperXzy/harbormaster/internal/ipinfo"
	"github.com/JasperXzy/harbormaster/internal/model"
	"github.com/JasperXzy/harbormaster/internal/netprobe"
)

const snapshotColumns = `s.seq,s.plan_id,s.plan_name,s.plan_revision,s.schedule_revision,s.target_id,s.target_revision,s.due_at,s.created_at,s.updated_at,s.trace_seq,r.job_id,s.skip_reason,coalesce(j.status,'skipped'),coalesce(j.finished_at,j.started_at),e.updated_at,e.state,s.notify_changes,s.comparison_json`
const snapshotJoins = ` FROM trace_snapshot_run s LEFT JOIN network_trace_run r ON r.seq=s.trace_seq LEFT JOIN job j ON j.id=r.job_id LEFT JOIN trace_enrichment e ON e.trace_seq=s.trace_seq `

func readTraceSnapshot(row rowScanner) (model.TraceSnapshot, error) {
	var v model.TraceSnapshot
	var seq int64
	var due, created, updated string
	var jobAt, enrichedAt, enrichment, comparison sql.NullString
	err := row.Scan(&seq, &v.PlanID, &v.PlanName, &v.PlanRevision, &v.ScheduleRevision, &v.TargetID, &v.TargetRevision, &due, &created, &updated, &v.TraceID, &v.JobID, &v.SkipReason, &v.Status, &jobAt, &enrichedAt, &enrichment, &v.NotifyChanges, &comparison)
	if err != nil {
		return v, err
	}
	v.ID = strconv.FormatInt(seq, 10)
	if v.DueAt, err = time.Parse(time.RFC3339Nano, due); err != nil {
		return v, err
	}
	if v.CreatedAt, err = time.Parse(time.RFC3339Nano, created); err != nil {
		return v, err
	}
	if v.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated); err != nil {
		return v, err
	}
	for _, raw := range []sql.NullString{jobAt, enrichedAt} {
		if at := parseAgentTime(raw); at != nil && at.After(v.UpdatedAt) {
			v.UpdatedAt = *at
		}
	}
	v.Comparison = model.PendingTraceComparison()
	if comparison.Valid {
		err = json.Unmarshal([]byte(comparison.String), &v.Comparison)
	} else if v.Status == "succeeded" {
		v.Comparison.Status = "waiting_enrichment"
	}
	return v, err
}

func (s *Store) TraceSnapshots(ctx context.Context, id string, from, to time.Time, before int64, limit int) ([]model.TraceSnapshot, error) {
	if !from.Before(to) || to.Sub(from) > 31*24*time.Hour || before < 0 || limit < 1 || limit > 201 {
		return nil, netprobe.ErrInvalid
	}
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var exists int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM probe_plan WHERE id=?", id).Scan(&exists); err != nil {
		return nil, err
	}
	if exists == 0 {
		return nil, ErrNotFound
	}
	rows, err := tx.QueryContext(ctx, "SELECT "+snapshotColumns+snapshotJoins+"WHERE s.plan_id=? AND s.created_at>=? AND s.created_at<? AND (?=0 OR s.seq<?) ORDER BY s.seq DESC LIMIT ?", id, timestamp(from), timestamp(to), before, before, limit)
	if err != nil {
		return nil, err
	}
	out := []model.TraceSnapshot{}
	for rows.Next() {
		v, e := readTraceSnapshot(rows)
		if e != nil {
			rows.Close()
			return nil, e
		}
		out = append(out, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	return out, tx.Commit()
}

// Only unfinished comparisons are evaluated. No list/detail GET calls here.
func evaluateTraceSnapshots(ctx context.Context, tx *sql.Tx, now time.Time) error {
	rows, err := tx.QueryContext(ctx, `SELECT s.seq`+snapshotJoins+`WHERE s.comparison_json IS NULL AND (s.trace_seq IS NULL OR
 (j.status IN ('failed','succeeded') AND (j.status='failed' OR e.state IN ('complete','partial','failed') OR e.trace_seq IS NULL))) ORDER BY s.seq LIMIT 100`)
	if err != nil {
		return err
	}
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err = evaluateTraceSnapshot(ctx, tx, id, now); err != nil {
			return err
		}
	}
	return nil
}

func evaluateTraceSnapshot(ctx context.Context, tx *sql.Tx, id int64, now time.Time) error {
	run, err := readTraceSnapshot(tx.QueryRowContext(ctx, "SELECT "+snapshotColumns+snapshotJoins+"WHERE s.seq=?", id))
	if err != nil {
		return err
	}
	c := model.PendingTraceComparison()
	c.Status = "not_comparable"
	var current model.TraceDetail
	if run.TraceID == nil {
		c.Reason = new("snapshot_skipped")
	} else {
		// Read the small outcome and frozen enrichment, never raw packet JSON.
		current, err = readTrace(tx.QueryRowContext(ctx, "SELECT "+traceColumns+",NULL,e.result_json"+traceJoins+"WHERE r.seq=?", *run.TraceID))
		if err != nil {
			return err
		}
		if current.Outcome != nil && current.Outcome.Address != "" {
			c.Address = &current.Outcome.Address
		}
		if current.Enrichment != nil {
			c.ASNDatabase = current.Enrichment.ASNDatabase
		}
		path, reason := model.TraceASPath(current)
		if reason != "" {
			c.Reason = &reason
		} else {
			c.ASNs = path
		}
	}
	prev, err := readTraceSnapshot(tx.QueryRowContext(ctx, "SELECT "+snapshotColumns+snapshotJoins+"WHERE s.plan_id=? AND s.seq<? ORDER BY s.seq DESC LIMIT 1", run.PlanID, id))
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil {
		c.PreviousSnapshotID = &prev.ID
		c.PreviousTraceID = prev.TraceID
		c.PreviousAddress = prev.Comparison.Address
		c.PreviousASNs = prev.Comparison.ASNs
		c.PreviousASNDatabase = prev.Comparison.ASNDatabase
	}
	if c.Reason == nil {
		switch {
		case errors.Is(err, sql.ErrNoRows):
			c.Status = "baseline"
			c.Reason = new("first_snapshot")
		case prev.Comparison.Status == "waiting_result" || prev.Comparison.Status == "waiting_enrichment":
			return nil
		case len(c.PreviousASNs) == 0 || c.PreviousAddress == nil:
			c.Reason = new("previous_not_comparable")
		case run.PlanRevision != prev.PlanRevision || run.TargetID != prev.TargetID || run.TargetRevision != prev.TargetRevision || run.ScheduleRevision != prev.ScheduleRevision:
			c.Reason = new("configuration_changed")
		case *c.Address != *c.PreviousAddress:
			c.Reason = new("target_changed")
		case !sameASNDatabase(c.ASNDatabase, c.PreviousASNDatabase):
			c.Reason = new("asn_database_changed")
		case slices.Equal(c.ASNs, c.PreviousASNs):
			c.Status = "unchanged"
		default:
			c.Status = "changed"
		}
	}
	if c.Status == "changed" {
		notice, err := notifyTraceChange(ctx, tx, run, current, now)
		if err != nil {
			return err
		}
		c.NotificationID = notice
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "UPDATE trace_snapshot_run SET comparison_json=?,updated_at=? WHERE seq=? AND comparison_json IS NULL", string(raw), timestamp(now), id)
	return err
}

func sameASNDatabase(a, b *ipinfo.Dataset) bool {
	return a != nil && b != nil && a.Name == b.Name && a.Edition == b.Edition && a.BuiltAt.Equal(b.BuiltAt) && a.SizeBytes == b.SizeBytes
}

func notifyTraceChange(ctx context.Context, tx *sql.Tx, run model.TraceSnapshot, current model.TraceDetail, now time.Time) (*string, error) {
	if !run.NotifyChanges {
		return nil, nil
	}
	plan, err := readProbePlan(ctx, tx, run.PlanID)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	spec, err := readTraceSchedule(ctx, tx, run.PlanID, "")
	if err != nil {
		return nil, err
	}
	if !spec.Active || !spec.NotifyChanges || spec.Revision != run.ScheduleRevision || plan.Revision != run.PlanRevision || current.Outcome == nil || now.Sub(current.Outcome.FinishedAt) > 10*time.Minute {
		return nil, nil
	}
	target, err := readTarget(ctx, tx, plan.TargetID)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if target.Revision != run.TargetRevision {
		return nil, nil
	}
	base, applicable, err := probeAlertContext(ctx, tx, plan, target)
	if err != nil || !applicable {
		return nil, err
	}
	rule, err := readAlertRule(tx.QueryRowContext(ctx, "SELECT key,enabled,params_json,urgent FROM alert_rule WHERE key='network_route_change'"))
	if err != nil || !rule.Enabled {
		return nil, err
	}
	n := model.Notification{EventKey: "trace-snapshot/" + run.ID, Kind: "alert", Severity: "info", RuleKey: &rule.Key, ProbePlanID: &run.PlanID, Title: severitySymbol("alert", "info") + " " + planCopyName(ctx, tx, run.PlanID) + " 的线路变了", Body: "每日线路快照和上一次相比，经过的网络（ASN）不同了。"}
	if base.account != "" {
		n.AccountID = &base.account
	}
	if base.instance != "" {
		n.InstanceID = &base.instance
	}
	n, err = enqueueNotification(ctx, tx, n, now)
	if err != nil {
		return nil, err
	}
	return &n.ID, nil
}
