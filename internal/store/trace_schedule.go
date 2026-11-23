package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
	"github.com/JasperXzy/harbormaster/internal/netprobe"
)

func readTraceSchedule(ctx context.Context, q queryRower, id, fallback string) (model.TraceSchedule, error) {
	v := model.DefaultTraceSchedule()
	v.PlanID = id
	plan, err := readProbePlan(ctx, q, id)
	if err != nil {
		return v, err
	}
	var raw, zone string
	var next int64
	err = q.QueryRowContext(ctx, "SELECT config_json,timezone,next_at FROM trace_schedule WHERE plan_id=?", id).Scan(&raw, &zone, &next)
	if errors.Is(err, sql.ErrNoRows) {
		loc, e := businessLocation(ctx, q, fallback)
		if e != nil {
			return v, e
		}
		v.Timezone = loc.String()
		return v, nil
	}
	if err != nil {
		return v, err
	}
	if err = json.Unmarshal([]byte(raw), &v); err != nil {
		return v, err
	}
	v.Timezone = zone
	v.Active = v.Enabled && plan.Enabled
	v.NextRunAt = nil
	if v.Active {
		at := time.Unix(next, 0).UTC()
		v.NextRunAt = &at
	}
	return v, nil
}
func (s *Store) TraceSchedule(ctx context.Context, id, fallback string) (model.TraceSchedule, error) {
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return model.TraceSchedule{}, err
	}
	defer tx.Rollback()
	v, err := readTraceSchedule(ctx, tx, id, fallback)
	if err != nil {
		return v, err
	}
	return v, tx.Commit()
}

func cancelTraceSnapshotWork(ctx context.Context, tx *sql.Tx, id string, now time.Time) error {
	_, err := tx.ExecContext(ctx, `UPDATE job SET status='failed',error_code='probe_configuration_changed',finished_at=?,lease_owner=NULL,lease_until=0
 WHERE status='queued' AND id IN (SELECT r.job_id FROM trace_snapshot_run s JOIN network_trace_run r ON r.seq=s.trace_seq WHERE s.plan_id=?)`, timestamp(now), id)
	if err != nil {
		return err
	}
	return cancelPendingNotifications(ctx, tx, "rule_key='network_route_change' AND probe_plan_id=?", []any{id}, "probe_configuration_changed", now)
}

// Editing a probe plan starts its trace configuration at a future daily time.
func resetTraceSchedule(ctx context.Context, tx *sql.Tx, id string, now time.Time) error {
	if err := cancelTraceSnapshotWork(ctx, tx, id, now); err != nil {
		return err
	}
	var raw, zone string
	err := tx.QueryRowContext(ctx, "SELECT config_json,timezone FROM trace_schedule WHERE plan_id=?", id).Scan(&raw, &zone)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var spec model.TraceSchedule
	if err = json.Unmarshal([]byte(raw), &spec); err != nil {
		return err
	}
	loc, err := time.LoadLocation(zone)
	if err != nil {
		return err
	}
	next, err := model.NextTraceTime(now, spec.TimeOfDay, loc)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "UPDATE trace_schedule SET next_at=? WHERE plan_id=?", next.Unix(), id)
	return err
}

func (s *Store) SaveTraceSchedule(ctx context.Context, id string, in model.TraceSchedule, fallback string, now time.Time) (model.TraceSchedule, error) {
	if !in.Validate() {
		return in, netprobe.ErrInvalid
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return in, err
	}
	defer tx.Rollback()
	old, err := readTraceSchedule(ctx, tx, id, fallback)
	if err != nil {
		return in, err
	}
	if old.Revision != in.Revision {
		return in, ErrNetworkChanged
	}
	plan, err := readProbePlan(ctx, tx, id)
	if err != nil {
		return in, err
	}
	if in.Enabled && !plan.Enabled {
		return in, &TracePreconditionError{Code: "plan_disabled"}
	}
	loc, err := businessLocation(ctx, tx, fallback)
	if err != nil {
		return in, err
	}
	next, err := model.NextTraceTime(now, in.TimeOfDay, loc)
	if err != nil {
		return in, err
	}
	if err = cancelTraceSnapshotWork(ctx, tx, id, now); err != nil {
		return in, err
	}
	in.PlanID, in.Revision, in.Timezone = id, in.Revision+1, loc.String()
	in.UpdatedAt = new(now.UTC())
	in.NextRunAt = nil
	in.Active = in.Enabled && plan.Enabled
	raw, err := json.Marshal(in)
	if err != nil {
		return in, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO trace_schedule(plan_id,revision,config_json,enabled,timezone,next_at) VALUES(?,?,?,?,?,?)
 ON CONFLICT(plan_id) DO UPDATE SET revision=excluded.revision,config_json=excluded.config_json,enabled=excluded.enabled,timezone=excluded.timezone,next_at=excluded.next_at`, id, in.Revision, string(raw), in.Enabled, in.Timezone, next.Unix())
	if err != nil {
		return in, err
	}
	if in.Active {
		in.NextRunAt = &next
	}
	details := auditDiff(auditChange{"enabled", old.Enabled, in.Enabled}, auditChange{"notify_changes", old.NotifyChanges, in.NotifyChanges}, auditChange{"time_of_day", old.TimeOfDay, in.TimeOfDay}, auditChange{"max_hops", old.MaxHops, in.MaxHops}, auditChange{"count", old.Count, in.Count}, auditChange{"timeout_ms", old.TimeoutMS, in.TimeoutMS}, auditChange{"timezone", old.Timezone, in.Timezone})
	details.Revision = &in.Revision
	if err = appendAudit(ctx, tx, auditEvent{Action: "trace_schedule_updated", TargetType: "trace_schedule", TargetID: id, Details: details}, now); err != nil {
		return in, err
	}
	return in, tx.Commit()
}

// One current opportunity per overdue schedule; missed days are never replayed.
func (s *Store) ScheduleTraceSnapshots(ctx context.Context, fallback string, now time.Time) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = expireNetworkProbes(ctx, tx, now); err != nil {
		return err
	}
	loc, err := businessLocation(ctx, tx, fallback)
	if err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT s.plan_id,s.next_at,s.timezone FROM trace_schedule s JOIN probe_plan p ON p.id=s.plan_id
 WHERE s.enabled=1 AND p.enabled=1 AND p.deleted=0 AND (s.next_at<=? OR s.timezone<>?) ORDER BY s.next_at,s.plan_id LIMIT 100`, now.Unix(), loc.String())
	if err != nil {
		return err
	}
	type due struct {
		id, zone string
		at       int64
	}
	items := []due{}
	for rows.Next() {
		var d due
		if err = rows.Scan(&d.id, &d.at, &d.zone); err != nil {
			rows.Close()
			return err
		}
		items = append(items, d)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	ctx = model.WithJobTrigger(ctx, "schedule")
	for _, d := range items {
		spec, err := readTraceSchedule(ctx, tx, d.id, fallback)
		if err != nil {
			return err
		}
		next, err := model.NextTraceTime(now, spec.TimeOfDay, loc)
		if err != nil {
			return err
		}
		// A timezone edit applies to future civil times, not an old UTC slot.
		if d.zone == loc.String() {
			if err = scheduleTraceSnapshot(ctx, tx, spec, time.Unix(d.at, 0).UTC(), now); err != nil {
				return err
			}
		}
		if _, err = tx.ExecContext(ctx, "UPDATE trace_schedule SET next_at=?,timezone=? WHERE plan_id=?", next.Unix(), loc.String(), d.id); err != nil {
			return err
		}
	}
	if err = evaluateTraceSnapshots(ctx, tx, now); err != nil {
		return err
	}
	return tx.Commit()
}

func scheduleTraceSnapshot(ctx context.Context, tx *sql.Tx, spec model.TraceSchedule, due, now time.Time) error {
	plan, err := readProbePlan(ctx, tx, spec.PlanID)
	if err != nil {
		return err
	}
	target, err := readTarget(ctx, tx, plan.TargetID)
	if err != nil {
		return err
	}
	in := model.TraceSubmission{RequestKey: model.NewID("snapshot_"), PointKind: plan.PointKind, InstanceID: plan.InstanceID, TargetID: &target.ID, TargetRevision: &target.Revision, Family: plan.Family, Method: plan.Method, MaxHops: spec.MaxHops, Count: spec.Count, TimeoutMS: spec.TimeoutMS}
	reason := ""
	if plan.Method == "tcp" {
		if target.TCPPort == nil {
			reason = "target_tcp_port_required"
		} else {
			in.Port = *target.TCPPort
		}
	}
	var active int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM trace_snapshot_run s JOIN network_trace_run r ON r.seq=s.trace_seq JOIN job j ON j.id=r.job_id WHERE s.plan_id=? AND j.status IN ('queued','running')`, plan.ID).Scan(&active); err != nil {
		return err
	}
	if active > 0 {
		reason = "previous_run_pending"
	}
	var traceID, skip any
	if reason == "" {
		accepted, e := submitTrace(ctx, tx, in, now)
		var precondition *TracePreconditionError
		switch {
		case e == nil:
			traceID = accepted.TraceID
		case errors.As(e, &precondition):
			reason = precondition.Code
		case errors.Is(e, ErrProbeBusy):
			reason = "probe_queue_full"
		case errors.Is(e, ErrNotFound), errors.Is(e, ErrAccountDisabled):
			reason = "point_unavailable"
		case errors.Is(e, netprobe.ErrInvalid):
			reason = "target_address_family_or_format"
		default:
			return e
		}
	}
	if reason != "" {
		skip = reason
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO trace_snapshot_run(plan_id,plan_name,plan_revision,schedule_revision,target_id,target_revision,due_at,created_at,updated_at,trace_seq,skip_reason,notify_changes)
 VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, plan.ID, plan.Name, plan.Revision, spec.Revision, target.ID, target.Revision, timestamp(due), timestamp(now), timestamp(now), traceID, skip, spec.NotifyChanges)
	return err
}
