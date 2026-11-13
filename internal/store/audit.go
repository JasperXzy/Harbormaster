package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
)

var ErrAuditFilter = errors.New("invalid_audit_filter")

// Values are compared in memory only. Only the fixed field names are persisted.
type auditChange struct {
	name          string
	before, after any
}

func auditChanges(changes ...auditChange) []string {
	fields := []string{}
	for _, change := range changes {
		if !reflect.DeepEqual(change.before, change.after) {
			fields = append(fields, change.name)
		}
	}
	return fields
}

type auditEvent struct {
	Action, TargetType, TargetID, AccountID, JobID string
	Details                                        model.AuditDetails
	Actor                                          *model.AuditActor
}

func appendAudit(ctx context.Context, tx *sql.Tx, event auditEvent, now time.Time) error {
	actor := model.AuditActorForContext(ctx)
	if event.Actor != nil {
		actor = *event.Actor
	}
	if !model.ValidAuditActor(actor.Kind) || event.Action == "" || event.TargetType == "" || event.TargetID == "" {
		return errors.New("invalid_audit_event")
	}
	if event.Details.ChangedFields == nil {
		event.Details.ChangedFields = []string{}
	}
	raw, err := json.Marshal(event.Details)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO audit_log(created_at,actor_kind,actor_id,action,target_type,target_id,account_id,job_id,details_json) VALUES(?,?,?,?,?,?,nullif(?,''),nullif(?,''),?)", timestamp(now), actor.Kind, actor.ID, event.Action, event.TargetType, event.TargetID, event.AccountID, event.JobID, string(raw))
	return err
}

const auditColumns = "seq,created_at,actor_kind,actor_id,action,target_type,target_id,account_id,job_id,details_json"

func readAudit(row rowScanner) (model.AuditEntry, error) {
	var e model.AuditEntry
	var seq int64
	var at, raw string
	err := row.Scan(&seq, &at, &e.Actor.Kind, &e.Actor.ID, &e.Action, &e.TargetType, &e.TargetID, &e.AccountID, &e.JobID, &raw)
	if err != nil {
		return e, err
	}
	e.ID = strconv.FormatInt(seq, 10)
	if e.CreatedAt, err = time.Parse(time.RFC3339Nano, at); err != nil {
		return e, err
	}
	if err = json.Unmarshal([]byte(raw), &e.Details); err != nil {
		return e, err
	}
	if e.Details.ChangedFields == nil {
		e.Details.ChangedFields = []string{}
	}
	e.Summary = "audit: " + e.Action
	return e, nil
}
func (s *Store) AuditLog(ctx context.Context, f model.AuditFilter) ([]model.AuditEntry, error) {
	if !f.Validate() {
		return nil, ErrAuditFilter
	}
	where := []string{}
	args := []any{}
	if f.Before > 0 {
		where = append(where, "seq<?")
		args = append(args, f.Before)
	}
	for _, pair := range []struct{ column, value string }{{"actor_kind", f.Actor}, {"action", f.Action}, {"target_type", f.TargetType}, {"target_id", f.TargetID}, {"account_id", f.AccountID}} {
		if pair.value != "" {
			where = append(where, pair.column+"=?")
			args = append(args, pair.value)
		}
	}
	if f.From != nil {
		where = append(where, "created_at>=? AND created_at<?")
		args = append(args, timestamp(*f.From), timestamp(*f.To))
	}
	query := "SELECT " + auditColumns + " FROM audit_log"
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	query += " ORDER BY seq DESC LIMIT ?"
	args = append(args, f.Limit)
	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.AuditEntry{}
	for rows.Next() {
		e, err := readAudit(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
