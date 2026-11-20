package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
	"github.com/JasperXzy/harbormaster/internal/netprobe"
)

var ErrNetworkChanged = errors.New("network_configuration_changed")
var ErrTargetInUse = errors.New("target_in_use")

func readTarget(ctx context.Context, q queryRower, id string) (model.NetworkTarget, error) {
	var v model.NetworkTarget
	var raw string
	err := q.QueryRowContext(ctx, "SELECT config_json FROM network_target WHERE id=? AND deleted=0", id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return v, ErrNotFound
	}
	if err != nil {
		return v, err
	}
	err = json.Unmarshal([]byte(raw), &v)
	return v, err
}
func (s *Store) NetworkTarget(ctx context.Context, id string) (model.NetworkTarget, error) {
	return readTarget(ctx, s.DB, id)
}
func (s *Store) NetworkTargets(ctx context.Context, after string, limit int) ([]model.NetworkTarget, error) {
	if limit < 1 || limit > 201 {
		return nil, netprobe.ErrInvalid
	}
	rows, err := s.DB.QueryContext(ctx, "SELECT config_json FROM network_target WHERE deleted=0 AND id>? ORDER BY id LIMIT ?", after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.NetworkTarget{}
	for rows.Next() {
		var raw string
		var v model.NetworkTarget
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(raw), &v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func requireInstance(ctx context.Context, q queryRower, id *string) error {
	if id == nil {
		return nil
	}
	var n int
	if err := q.QueryRowContext(ctx, "SELECT count(*) FROM instance WHERE id=?", *id).Scan(&n); err != nil {
		return err
	}
	if n != 1 {
		return ErrNotFound
	}
	return nil
}
func cancelQueuedProbeRuns(ctx context.Context, tx *sql.Tx, column, id string, now time.Time) error {
	if column != "plan_id" && column != "target_id" {
		return netprobe.ErrInvalid
	}
	_, err := tx.ExecContext(ctx, "UPDATE job SET status='failed',error_code='probe_configuration_changed',finished_at=?,lease_owner=NULL,lease_until=0 WHERE status='queued' AND id IN (SELECT job_id FROM probe_plan_run WHERE "+column+"=?)", timestamp(now), id)
	if err == nil && column == "target_id" {
		_, err = tx.ExecContext(ctx, "UPDATE job SET status='failed',error_code='probe_configuration_changed',finished_at=?,lease_owner=NULL,lease_until=0 WHERE status='queued' AND id IN (SELECT job_id FROM network_trace_run WHERE target_id=?)", timestamp(now), id)
		if err == nil {
			err = cancelPendingNotifications(ctx, tx, "rule_key='network_route_change' AND probe_plan_id IN (SELECT id FROM probe_plan WHERE target_id=?)", []any{id}, "probe_configuration_changed", now)
		}
	}
	return err
}
func (s *Store) SaveNetworkTarget(ctx context.Context, id string, in model.NetworkTarget, now time.Time) (model.NetworkTarget, error) {
	if !in.Validate() {
		return in, netprobe.ErrInvalid
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return in, err
	}
	defer tx.Rollback()
	if err = requireInstance(ctx, tx, in.InstanceID); err != nil {
		return in, err
	}
	previous := model.NetworkTarget{}
	if id == "" {
		if in.Revision != 0 {
			return in, ErrNetworkChanged
		}
		in.ID = model.NewID("target_")
		in.CreatedAt = now.UTC()
	} else {
		previous, err = readTarget(ctx, tx, id)
		if err != nil {
			return in, err
		}
		if previous.Revision != in.Revision {
			return in, ErrNetworkChanged
		}
		in.ID = id
		in.CreatedAt = previous.CreatedAt
		if err = updateProbeTargetAlerts(ctx, tx, previous, in, now); err != nil {
			return in, err
		}
		if err = cancelQueuedProbeRuns(ctx, tx, "target_id", id, now); err != nil {
			return in, err
		}
	}
	in.Revision++
	in.UpdatedAt = now.UTC()
	raw, err := json.Marshal(in)
	if err != nil {
		return in, err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO network_target(id,revision,config_json,enabled) VALUES(?,?,?,?) ON CONFLICT(id) DO UPDATE SET revision=excluded.revision,config_json=excluded.config_json,enabled=excluded.enabled", in.ID, in.Revision, string(raw), in.Enabled)
	if err != nil {
		return in, err
	}
	action := "probe_target_updated"
	details := auditDiff(auditChange{"name", previous.Name, in.Name}, auditChange{"kind", previous.Kind, in.Kind}, auditChange{"host", previous.Host, in.Host}, auditChange{"instance_id", previous.InstanceID, in.InstanceID}, auditChange{"tcp_port", previous.TCPPort, in.TCPPort}, auditChange{"enabled", previous.Enabled, in.Enabled})
	if id == "" {
		action = "probe_target_created"
		details = model.AuditDetails{
			ChangedFields: []string{"name", "kind", "host", "instance_id", "tcp_port", "enabled"},
			After:         map[string]any{"name": in.Name, "kind": in.Kind, "host": in.Host, "instance_id": in.InstanceID, "tcp_port": in.TCPPort, "enabled": in.Enabled},
		}
	}
	details.Revision = &in.Revision
	if err = appendAudit(ctx, tx, auditEvent{Action: action, TargetType: "probe_target", TargetID: in.ID, Details: details}, now); err != nil {
		return in, err
	}
	return in, tx.Commit()
}
func (s *Store) DeleteNetworkTarget(ctx context.Context, id string, revision int64, now time.Time) error {
	if revision < 0 {
		return netprobe.ErrInvalid
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	current, err := readTarget(ctx, tx, id)
	if err != nil {
		return err
	}
	if current.Revision != revision {
		return ErrNetworkChanged
	}
	var n int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM probe_plan WHERE target_id=? AND deleted=0", id).Scan(&n); err != nil {
		return err
	}
	if n != 0 {
		return ErrTargetInUse
	}
	if err = cancelQueuedProbeRuns(ctx, tx, "target_id", id, now); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE network_target SET deleted=1,enabled=0,revision=revision+1 WHERE id=?", id); err != nil {
		return err
	}
	if err = appendAudit(ctx, tx, auditEvent{Action: "probe_target_deleted", TargetType: "probe_target", TargetID: id, Details: model.AuditDetails{ChangedFields: auditChanges(auditChange{"deleted", false, true}, auditChange{"enabled", current.Enabled, false}), Revision: new(revision + 1)}}, now); err != nil {
		return err
	}
	return tx.Commit()
}
func readProbePlan(ctx context.Context, q queryRower, id string) (model.ProbePlan, error) {
	var v model.ProbePlan
	var raw string
	var next int64
	err := q.QueryRowContext(ctx, "SELECT config_json,next_at FROM probe_plan WHERE id=? AND deleted=0", id).Scan(&raw, &next)
	if errors.Is(err, sql.ErrNoRows) {
		return v, ErrNotFound
	}
	if err != nil {
		return v, err
	}
	if err = json.Unmarshal([]byte(raw), &v); err != nil {
		return v, err
	}
	if v.Enabled {
		at := time.Unix(next, 0).UTC()
		v.NextRunAt = &at
	}
	return v, nil
}
func (s *Store) ProbePlan(ctx context.Context, id string) (model.ProbePlan, error) {
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return model.ProbePlan{}, err
	}
	defer tx.Rollback()
	v, err := readProbePlan(ctx, tx, id)
	if err != nil {
		return v, err
	}
	v.LastRun, err = latestProbeRun(ctx, tx, id)
	if err != nil {
		return v, err
	}
	return v, tx.Commit()
}
func (s *Store) ProbePlans(ctx context.Context, after string, limit int) ([]model.ProbePlan, error) {
	if limit < 1 || limit > 201 {
		return nil, netprobe.ErrInvalid
	}
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, "SELECT id FROM probe_plan WHERE deleted=0 AND id>? ORDER BY id LIMIT ?", after, limit)
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	out := []model.ProbePlan{}
	for _, id := range ids {
		v, err := readProbePlan(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		v.LastRun, err = latestProbeRun(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, tx.Commit()
}
func (s *Store) SaveProbePlan(ctx context.Context, id string, in model.ProbePlan, now time.Time) (model.ProbePlan, error) {
	if !in.Validate() {
		return in, netprobe.ErrInvalid
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return in, err
	}
	defer tx.Rollback()
	if _, err = readTarget(ctx, tx, in.TargetID); err != nil {
		return in, err
	}
	if err = requireInstance(ctx, tx, in.InstanceID); err != nil {
		return in, err
	}
	previous := model.ProbePlan{}
	if id == "" {
		if in.Revision != 0 {
			return in, ErrNetworkChanged
		}
		in.ID = model.NewID("plan_")
		in.CreatedAt = now.UTC()
	} else {
		previous, err = readProbePlan(ctx, tx, id)
		if err != nil {
			return in, err
		}
		if previous.Revision != in.Revision {
			return in, ErrNetworkChanged
		}
		in.ID = id
		in.CreatedAt = previous.CreatedAt
		if err = updateProbePlanAlerts(ctx, tx, previous, in, now); err != nil {
			return in, err
		}
		if err = cancelQueuedProbeRuns(ctx, tx, "plan_id", id, now); err != nil {
			return in, err
		}
		if err = resetTraceSchedule(ctx, tx, id, now); err != nil {
			return in, err
		}
	}
	in.Revision++
	in.UpdatedAt = now.UTC()
	in.LastRun = nil
	in.NextRunAt = nil
	raw, err := json.Marshal(in)
	if err != nil {
		return in, err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO probe_plan(id,revision,target_id,config_json,enabled,next_at) VALUES(?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET revision=excluded.revision,target_id=excluded.target_id,config_json=excluded.config_json,enabled=excluded.enabled,next_at=excluded.next_at", in.ID, in.Revision, in.TargetID, string(raw), in.Enabled, now.Unix())
	if err != nil {
		return in, err
	}
	if in.Enabled {
		at := time.Unix(now.Unix(), 0).UTC()
		in.NextRunAt = &at
	}
	in.LastRun, err = latestProbeRun(ctx, tx, in.ID)
	if err != nil {
		return in, err
	}
	action := "probe_plan_updated"
	details := auditDiff(auditChange{"name", previous.Name, in.Name}, auditChange{"point_kind", previous.PointKind, in.PointKind}, auditChange{"instance_id", previous.InstanceID, in.InstanceID}, auditChange{"target_id", previous.TargetID, in.TargetID}, auditChange{"family", previous.Family, in.Family}, auditChange{"method", previous.Method, in.Method}, auditChange{"frequency_seconds", previous.FrequencySeconds, in.FrequencySeconds}, auditChange{"count", previous.Count, in.Count}, auditChange{"timeout_ms", previous.TimeoutMS, in.TimeoutMS}, auditChange{"interval_ms", previous.IntervalMS, in.IntervalMS}, auditChange{"enabled", previous.Enabled, in.Enabled})
	if id == "" {
		action = "probe_plan_created"
		details = model.AuditDetails{
			ChangedFields: []string{"name", "point_kind", "instance_id", "target_id", "family", "method", "frequency_seconds", "count", "timeout_ms", "interval_ms", "enabled"},
			After:         map[string]any{"name": in.Name, "point_kind": in.PointKind, "instance_id": in.InstanceID, "target_id": in.TargetID, "family": in.Family, "method": in.Method, "frequency_seconds": in.FrequencySeconds, "count": in.Count, "timeout_ms": in.TimeoutMS, "interval_ms": in.IntervalMS, "enabled": in.Enabled},
		}
	}
	details.Revision = &in.Revision
	if err = appendAudit(ctx, tx, auditEvent{Action: action, TargetType: "probe_plan", TargetID: in.ID, Details: details}, now); err != nil {
		return in, err
	}
	return in, tx.Commit()
}
func (s *Store) DeleteProbePlan(ctx context.Context, id string, revision int64, now time.Time) error {
	if revision < 0 {
		return netprobe.ErrInvalid
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	current, err := readProbePlan(ctx, tx, id)
	if err != nil {
		return err
	}
	if current.Revision != revision {
		return ErrNetworkChanged
	}
	if err = closeProbePlanAlerts(ctx, tx, id, now); err != nil {
		return err
	}
	if err = cancelQueuedProbeRuns(ctx, tx, "plan_id", id, now); err != nil {
		return err
	}
	if err = cancelTraceSnapshotWork(ctx, tx, id, now); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE probe_plan SET deleted=1,enabled=0,revision=revision+1 WHERE id=?", id); err != nil {
		return err
	}
	if err = appendAudit(ctx, tx, auditEvent{Action: "probe_plan_deleted", TargetType: "probe_plan", TargetID: id, Details: model.AuditDetails{ChangedFields: auditChanges(auditChange{"deleted", false, true}, auditChange{"enabled", current.Enabled, false}), Revision: new(revision + 1)}}, now); err != nil {
		return err
	}
	return tx.Commit()
}

const probeRunColumns = `r.seq,r.plan_id,r.plan_revision,r.target_id,r.target_revision,r.point_kind,r.instance_id,r.created_at,r.job_id,r.skip_reason,coalesce(j.status,'skipped'),j.error_code,coalesce(j.finished_at,j.started_at),p.request_json,p.result_json`
const probeRunJoins = ` FROM probe_plan_run r LEFT JOIN job j ON j.id=r.job_id LEFT JOIN network_probe_job p ON p.job_id=r.job_id `

func readProbeRun(row rowScanner) (model.ProbeRun, error) {
	var v model.ProbeRun
	var seq int64
	var at string
	var finished, request, result, code sql.NullString
	err := row.Scan(&seq, &v.PlanID, &v.PlanRevision, &v.TargetID, &v.TargetRevision, &v.PointKind, &v.InstanceID, &at, &v.JobID, &v.SkipReason, &v.Status, &code, &finished, &request, &result)
	if err != nil {
		return v, err
	}
	v.ID = strconv.FormatInt(seq, 10)
	v.CreatedAt, err = time.Parse(time.RFC3339Nano, at)
	if err != nil {
		return v, err
	}
	v.UpdatedAt = v.CreatedAt
	if done := parseAgentTime(finished); done != nil {
		v.UpdatedAt = *done
	}
	if code.Valid {
		v.Error = model.Error(code.String)
	}
	if request.Valid {
		v.Request = new(netprobe.Request)
		if err = json.Unmarshal([]byte(request.String), v.Request); err != nil {
			return v, err
		}
	}
	if result.Valid {
		var wrapped struct {
			Result netprobe.Result `json:"result"`
		}
		if err = json.Unmarshal([]byte(result.String), &wrapped); err != nil {
			return v, err
		}
		v.Result = &wrapped.Result
	}
	return v, nil
}
func latestProbeRun(ctx context.Context, q queryRower, id string) (*model.ProbeRun, error) {
	v, err := readProbeRun(q.QueryRowContext(ctx, "SELECT "+probeRunColumns+probeRunJoins+"WHERE r.plan_id=? ORDER BY r.seq DESC LIMIT 1", id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &v, err
}
func (s *Store) ProbeRuns(ctx context.Context, id string, from, to time.Time, before int64, limit int) ([]model.ProbeRun, error) {
	if limit < 1 || limit > 201 || before < 0 || !from.Before(to) || to.Sub(from) > 7*24*time.Hour {
		return nil, netprobe.ErrInvalid
	}
	var n int
	if err := s.DB.QueryRowContext(ctx, "SELECT count(*) FROM probe_plan WHERE id=?", id).Scan(&n); err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, ErrNotFound
	}
	rows, err := s.DB.QueryContext(ctx, "SELECT "+probeRunColumns+probeRunJoins+"WHERE r.plan_id=? AND r.created_at>=? AND r.created_at<? AND (?=0 OR r.seq<?) ORDER BY r.seq DESC LIMIT ?", id, timestamp(from), timestamp(to), before, before, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.ProbeRun{}
	for rows.Next() {
		v, err := readProbeRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
