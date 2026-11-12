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

// Codes come only from local point/target checks, never provider error text.
type TracePreconditionError struct{ Code string }

func (e *TracePreconditionError) Error() string { return e.Code }

func (s *Store) SubmitTrace(ctx context.Context, in model.TraceSubmission, now time.Time) (model.TraceAccepted, error) {
	var out model.TraceAccepted
	if !in.Validate() {
		return out, netprobe.ErrInvalid
	}
	input, err := json.Marshal(in)
	if err != nil {
		return out, netprobe.ErrInvalid
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	var oldJob string
	var oldSeq sql.NullInt64
	var oldInput sql.NullString
	err = tx.QueryRowContext(ctx, "SELECT p.job_id,r.seq,r.input_json FROM network_probe_job p LEFT JOIN network_trace_run r ON r.job_id=p.job_id WHERE p.request_key=?", in.RequestKey).Scan(&oldJob, &oldSeq, &oldInput)
	if err == nil {
		if !oldSeq.Valid || oldInput.String != string(input) {
			return out, ErrConflict
		}
		return model.TraceAccepted{JobID: oldJob, TraceID: strconv.FormatInt(oldSeq.Int64, 10)}, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return out, err
	}
	if err = expireNetworkProbes(ctx, tx, now); err != nil {
		return out, err
	}
	pointID := "server"
	if in.InstanceID != nil {
		pointID = *in.InstanceID
	}
	point, err := probePoint(ctx, tx, pointID, now)
	if err != nil {
		return out, err
	}
	if point.PointKind != in.PointKind {
		return out, ErrNotFound
	}
	if !point.Trace.Available {
		reason := "point_unavailable"
		if point.Trace.UnavailableReason != nil {
			reason = *point.Trace.UnavailableReason
		}
		return out, &TracePreconditionError{Code: reason}
	}
	supported := false
	for _, method := range point.Trace.Methods {
		if method == in.Method {
			supported = true
		}
	}
	if !supported {
		return out, &TracePreconditionError{Code: "trace_method_unavailable"}
	}
	var targetJSON any
	host := ""
	if in.TargetID != nil {
		target, err := readTarget(ctx, tx, *in.TargetID)
		if err != nil {
			return out, err
		}
		if target.Revision != *in.TargetRevision {
			return out, ErrNetworkChanged
		}
		var reason string
		host, reason, err = resolveProbeTarget(ctx, tx, target, in.Family, now)
		if err != nil {
			return out, err
		}
		if reason != "" {
			return out, &TracePreconditionError{Code: reason}
		}
		raw, err := json.Marshal(target)
		if err != nil {
			return out, err
		}
		targetJSON = string(raw)
	} else {
		host = *in.Host
	}
	request := in.Request(host)
	if request.Validate() != nil {
		return out, netprobe.ErrInvalid
	}
	var job model.Job
	if in.PointKind == "server" {
		job, err = enqueueServerNetwork(ctx, tx, in.RequestKey, "trace", request, now)
	} else {
		job, err = enqueueAgentNetwork(ctx, tx, *in.InstanceID, in.RequestKey, "trace", request, now)
	}
	if err != nil {
		return out, err
	}
	insert, err := tx.ExecContext(ctx, "INSERT INTO network_trace_run(job_id,input_json,point_id,point_name,target_id,target_revision,target_json,created_at) VALUES(?,?,?,?,?,?,?,?)", job.ID, string(input), pointID, point.Name, in.TargetID, in.TargetRevision, targetJSON, timestamp(now))
	if err != nil {
		return out, err
	}
	seq, err := insert.LastInsertId()
	if err != nil {
		return out, err
	}
	out = model.TraceAccepted{JobID: job.ID, TraceID: strconv.FormatInt(seq, 10)}
	return out, tx.Commit()
}

// Persist the small list projection in the same transaction as the raw result.
// Private queue tasks without a panel history row simply update zero rows.
func saveTraceOutcome(ctx context.Context, tx *sql.Tx, id, raw string, now time.Time) error {
	var wrapped struct {
		Result netprobe.TraceResult `json:"result"`
	}
	if err := json.Unmarshal([]byte(raw), &wrapped); err != nil {
		return err
	}
	projection, err := json.Marshal(model.SummarizeTrace(wrapped.Result))
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "UPDATE network_trace_run SET outcome_json=? WHERE job_id=?", string(projection), id)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "INSERT OR IGNORE INTO trace_enrichment(trace_seq,state,updated_at) SELECT seq,'queued',? FROM network_trace_run WHERE job_id=?", timestamp(now), id)
	return err
}

const traceColumns = `r.seq,r.job_id,r.point_id,r.point_name,p.point_kind,p.instance_id,r.target_json,p.request_json,r.created_at,coalesce(j.finished_at,j.started_at),j.status,j.error_code,r.outcome_json,coalesce(e.state,CASE WHEN j.status IN ('queued','running') THEN 'waiting_result' ELSE 'not_available' END),e.updated_at,e.error_code`
const traceJoins = ` FROM network_trace_run r JOIN network_probe_job p ON p.job_id=r.job_id JOIN job j ON j.id=r.job_id LEFT JOIN trace_enrichment e ON e.trace_seq=r.seq `

func readTrace(row rowScanner) (model.TraceDetail, error) {
	var d model.TraceDetail
	var seq int64
	var created, request string
	var target, updated, code, outcome, result sql.NullString
	var enrichmentUpdated, enrichmentCode, enrichment sql.NullString
	err := row.Scan(&seq, &d.JobID, &d.PointID, &d.PointName, &d.PointKind, &d.InstanceID, &target, &request, &created, &updated, &d.Status, &code, &outcome, &d.EnrichmentStatus, &enrichmentUpdated, &enrichmentCode, &result, &enrichment)
	if err != nil {
		return d, err
	}
	d.ID = strconv.FormatInt(seq, 10)
	d.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
	if err != nil {
		return d, err
	}
	d.UpdatedAt = d.CreatedAt
	if t := parseAgentTime(updated); t != nil {
		d.UpdatedAt = *t
	}
	if t := parseAgentTime(enrichmentUpdated); t != nil && t.After(d.UpdatedAt) {
		d.UpdatedAt = *t
	}
	if enrichmentCode.Valid {
		d.EnrichmentError = model.Error(enrichmentCode.String)
	}
	if enrichment.Valid {
		d.Enrichment = new(model.TraceEnrichment)
		if err = json.Unmarshal([]byte(enrichment.String), d.Enrichment); err != nil {
			return d, err
		}
	}
	if code.Valid {
		d.Error = model.Error(code.String)
	}
	if err = json.Unmarshal([]byte(request), &d.Request); err != nil {
		return d, err
	}
	if target.Valid {
		d.Target = new(model.NetworkTarget)
		if err = json.Unmarshal([]byte(target.String), d.Target); err != nil {
			return d, err
		}
	}
	if outcome.Valid {
		d.Outcome = new(model.TraceOutcome)
		if err = json.Unmarshal([]byte(outcome.String), d.Outcome); err != nil {
			return d, err
		}
	}
	if result.Valid {
		var wrapped struct {
			Result netprobe.TraceResult `json:"result"`
		}
		if err = json.Unmarshal([]byte(result.String), &wrapped); err != nil {
			return d, err
		}
		d.Result = &wrapped.Result
	}
	return d, nil
}
func (s *Store) Trace(ctx context.Context, id string) (model.TraceDetail, error) {
	seq, err := strconv.ParseInt(id, 10, 64)
	if err != nil || seq < 1 {
		return model.TraceDetail{}, ErrNotFound
	}
	d, err := readTrace(s.DB.QueryRowContext(ctx, "SELECT "+traceColumns+",p.result_json,e.result_json"+traceJoins+"WHERE r.seq=?", seq))
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}
	return d, err
}
func (s *Store) Traces(ctx context.Context, f model.TraceFilter) ([]model.TraceRun, error) {
	if !f.Validate() {
		return nil, netprobe.ErrInvalid
	}
	rows, err := s.DB.QueryContext(ctx, "SELECT "+traceColumns+",NULL,NULL"+traceJoins+"WHERE r.created_at>=? AND r.created_at<? AND (?='' OR r.point_id=?) AND (?='' OR r.target_id=?) AND (?=0 OR r.seq<?) ORDER BY r.seq DESC LIMIT ?", timestamp(f.From), timestamp(f.To), f.PointID, f.PointID, f.TargetID, f.TargetID, f.Before, f.Before, f.Limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.TraceRun{}
	for rows.Next() {
		d, err := readTrace(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d.TraceRun)
	}
	return out, rows.Err()
}

func validateQueuedTraceTargets(ctx context.Context, tx *sql.Tx, now time.Time) error {
	rows, err := tx.QueryContext(ctx, `SELECT r.job_id,r.target_id,r.target_revision,p.request_json FROM network_trace_run r JOIN network_probe_job p ON p.job_id=r.job_id JOIN job j ON j.id=r.job_id WHERE r.target_id IS NOT NULL AND j.status='queued'`)
	if err != nil {
		return err
	}
	type pending struct {
		job, target, raw string
		revision         int64
	}
	items := []pending{}
	for rows.Next() {
		var item pending
		if err = rows.Scan(&item.job, &item.target, &item.revision, &item.raw); err != nil {
			rows.Close()
			return err
		}
		items = append(items, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, item := range items {
		var request netprobe.TraceRequest
		if json.Unmarshal([]byte(item.raw), &request) != nil {
			return netprobe.ErrInvalid
		}
		target, err := readTarget(ctx, tx, item.target)
		if err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}
		changed := err != nil || target.Revision != item.revision
		if !changed {
			host, reason, err := resolveProbeTarget(ctx, tx, target, request.Family, now)
			if err != nil {
				return err
			}
			changed = reason != "" || host != request.Host
		}
		if changed {
			if _, err = tx.ExecContext(ctx, "UPDATE job SET status='failed',error_code='probe_target_changed',finished_at=?,lease_owner=NULL,lease_until=0 WHERE id=? AND status='queued'", timestamp(now), item.job); err != nil {
				return err
			}
		}
	}
	return nil
}
