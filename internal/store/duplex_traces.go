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

func readDuplexEndpoint(ctx context.Context, q queryRower, id, family string, now time.Time) (model.DuplexTraceEndpoint, error) {
	v := model.DuplexTraceEndpoint{InstanceID: id}
	var observed sql.NullString
	err := q.QueryRowContext(ctx, `SELECT i.name,i.kind,c.observed_at FROM instance i LEFT JOIN instance_probe_address c ON c.instance_id=i.id WHERE i.id=?`, id).Scan(&v.Name, &v.Kind, &observed)
	if errors.Is(err, sql.ErrNoRows) {
		return v, ErrNotFound
	}
	if err != nil {
		return v, err
	}
	if v.Kind != "ecs" && v.Kind != "swas" {
		return v, ErrNotFound
	}
	host, reason, err := resolveProbeTarget(ctx, q, model.NetworkTarget{Kind: "instance", InstanceID: &id, Enabled: true}, family, now)
	if err != nil {
		return v, err
	}
	if reason != "" {
		return v, &TracePreconditionError{Code: reason}
	}
	at := parseAgentTime(observed)
	if at == nil {
		return v, &TracePreconditionError{Code: "target_discovery_incomplete"}
	}
	v.PublicAddress, v.ObservedAt = host, *at
	return v, nil
}

func (s *Store) SubmitDuplexTrace(ctx context.Context, in model.DuplexTraceSubmission, now time.Time) (model.DuplexTraceAccepted, error) {
	var out model.DuplexTraceAccepted
	if !in.Validate() {
		return out, netprobe.ErrInvalid
	}
	raw, err := json.Marshal(in)
	if err != nil {
		return out, netprobe.ErrInvalid
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	var oldInput string
	err = tx.QueryRowContext(ctx, "SELECT id,input_json FROM duplex_trace WHERE request_key=?", in.RequestKey).Scan(&out.DuplexID, &oldInput)
	if err == nil {
		if oldInput != string(raw) {
			return out, ErrConflict
		}
		out, err = readDuplexAccepted(ctx, tx, out.DuplexID)
		if err != nil {
			return out, err
		}
		return out, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return out, err
	}
	from, err := readDuplexEndpoint(ctx, tx, in.FromInstanceID, in.Family, now)
	if err != nil {
		return out, err
	}
	to, err := readDuplexEndpoint(ctx, tx, in.ToInstanceID, in.Family, now)
	if err != nil {
		return out, err
	}
	if from.PublicAddress == to.PublicAddress {
		return out, &TracePreconditionError{Code: "duplex_same_address"}
	}
	fromJSON, _ := json.Marshal(from)
	toJSON, _ := json.Marshal(to)
	out.DuplexID = model.NewID("duplex_")
	if _, err = tx.ExecContext(ctx, "INSERT INTO duplex_trace(id,request_key,input_json,from_instance_id,to_instance_id,from_json,to_json,created_at) VALUES(?,?,?,?,?,?,?,?)", out.DuplexID, in.RequestKey, string(raw), from.InstanceID, to.InstanceID, string(fromJSON), string(toJSON), timestamp(now)); err != nil {
		return out, err
	}
	for _, direction := range []string{"forward", "reverse"} {
		source, target, port := from, to, in.ForwardPort
		if direction == "reverse" {
			source, target, port = to, from, in.ReversePort
		}
		leg := model.TraceSubmission{RequestKey: model.NewID("duplex_leg_"), PointKind: "agent", InstanceID: &source.InstanceID, Host: &target.PublicAddress, Family: in.Family, Method: in.Method, Port: port, MaxHops: in.MaxHops, Count: in.Count, TimeoutMS: in.TimeoutMS}
		accepted, err := submitTrace(ctx, tx, leg, now)
		if err != nil {
			return out, err
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO duplex_trace_leg(duplex_id,direction,trace_seq) VALUES(?,?,?)", out.DuplexID, direction, accepted.TraceID); err != nil {
			return out, err
		}
		if direction == "forward" {
			out.Forward = accepted
		} else {
			out.Reverse = accepted
		}
	}
	return out, tx.Commit()
}

func readDuplexAccepted(ctx context.Context, q queryRower, id string) (model.DuplexTraceAccepted, error) {
	out := model.DuplexTraceAccepted{DuplexID: id}
	for _, direction := range []string{"forward", "reverse"} {
		var a model.TraceAccepted
		err := q.QueryRowContext(ctx, "SELECT r.job_id,l.trace_seq FROM duplex_trace_leg l JOIN network_trace_run r ON r.seq=l.trace_seq WHERE l.duplex_id=? AND l.direction=?", id, direction).Scan(&a.JobID, &a.TraceID)
		if err != nil {
			return out, err
		}
		if direction == "forward" {
			out.Forward = a
		} else {
			out.Reverse = a
		}
	}
	return out, nil
}

func (s *Store) DuplexTrace(ctx context.Context, id string) (model.DuplexTrace, error) {
	var d model.DuplexTrace
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return d, err
	}
	defer tx.Rollback()
	var created, fromJSON, toJSON string
	err = tx.QueryRowContext(ctx, "SELECT id,created_at,from_json,to_json FROM duplex_trace WHERE id=?", id).Scan(&d.ID, &created, &fromJSON, &toJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return d, ErrNotFound
	}
	if err != nil {
		return d, err
	}
	if d.CreatedAt, err = time.Parse(time.RFC3339Nano, created); err != nil {
		return d, err
	}
	if err = json.Unmarshal([]byte(fromJSON), &d.From); err != nil {
		return d, err
	}
	if err = json.Unmarshal([]byte(toJSON), &d.To); err != nil {
		return d, err
	}
	accepted, err := readDuplexAccepted(ctx, tx, id)
	if err != nil {
		return d, err
	}
	d.Forward, err = readTrace(tx.QueryRowContext(ctx, "SELECT "+traceColumns+",p.result_json,e.result_json"+traceJoins+"WHERE r.seq=?", accepted.Forward.TraceID))
	if err != nil {
		return d, err
	}
	d.Reverse, err = readTrace(tx.QueryRowContext(ctx, "SELECT "+traceColumns+",p.result_json,e.result_json"+traceJoins+"WHERE r.seq=?", accepted.Reverse.TraceID))
	if err != nil {
		return d, err
	}
	return d, tx.Commit()
}

// Both endpoint addresses define the pair. A changed or unavailable cached
// endpoint invalidates only still-queued legs; running observations stay pinned.
// The other agent going offline alone never replays/cancels a finished leg.
func validateQueuedDuplexTraces(ctx context.Context, tx *sql.Tx, now time.Time) error {
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT d.id,d.from_json,d.to_json,json_extract(d.input_json,'$.family') FROM duplex_trace d JOIN duplex_trace_leg l ON l.duplex_id=d.id JOIN network_trace_run r ON r.seq=l.trace_seq JOIN job j ON j.id=r.job_id WHERE j.type='network_probe' AND j.status='queued'`)
	if err != nil {
		return err
	}
	type pending struct{ id, from, to, family string }
	items := []pending{}
	for rows.Next() {
		var p pending
		if err = rows.Scan(&p.id, &p.from, &p.to, &p.family); err != nil {
			rows.Close()
			return err
		}
		items = append(items, p)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, p := range items {
		var from, to model.DuplexTraceEndpoint
		if json.Unmarshal([]byte(p.from), &from) != nil || json.Unmarshal([]byte(p.to), &to) != nil {
			return netprobe.ErrInvalid
		}
		changed := false
		for _, old := range []model.DuplexTraceEndpoint{from, to} {
			fresh, err := readDuplexEndpoint(ctx, tx, old.InstanceID, p.family, now)
			var condition *TracePreconditionError
			if errors.Is(err, ErrNotFound) || errors.As(err, &condition) {
				changed = true
				continue
			}
			if err != nil {
				return err
			}
			changed = changed || fresh.PublicAddress != old.PublicAddress
		}
		if changed {
			if _, err = tx.ExecContext(ctx, `UPDATE job SET status='failed',error_code='probe_target_changed',finished_at=?,lease_owner=NULL,lease_until=0 WHERE status='queued' AND id IN (SELECT r.job_id FROM duplex_trace_leg l JOIN network_trace_run r ON r.seq=l.trace_seq WHERE l.duplex_id=?)`, timestamp(now), p.id); err != nil {
				return err
			}
		}
	}
	return nil
}
