package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
)

const jobColumns = "id,type,status,created_at,started_at,finished_at,error_code,result_json,account_ids_json,coalesce(lease_owner,''),attempts,scope,trigger_source,instance_id,(SELECT seq FROM job_order WHERE job_id=job.id)"

type rowScanner interface{ Scan(...any) error }

func readJob(row rowScanner) (model.Job, error) {
	var j model.Job
	var created string
	var started, finished, code, result sql.NullString
	var ids string
	err := row.Scan(&j.ID, &j.Type, &j.Status, &created, &started, &finished, &code, &result, &ids, &j.Owner, &j.Attempts, &j.Scope, &j.Trigger, &j.InstanceID, &j.Sequence)
	if errors.Is(err, sql.ErrNoRows) {
		return j, ErrNotFound
	}
	if err != nil {
		return j, err
	}
	if j.CreatedAt, err = time.Parse(time.RFC3339Nano, created); err != nil {
		return j, err
	}
	if started.Valid {
		t, e := time.Parse(time.RFC3339Nano, started.String)
		if e != nil {
			return j, e
		}
		j.StartedAt = &t
	}
	if finished.Valid {
		t, e := time.Parse(time.RFC3339Nano, finished.String)
		if e != nil {
			return j, e
		}
		j.FinishedAt = &t
	}
	if code.Valid {
		j.Error = model.Error(code.String)
	}
	if result.Valid {
		if err = json.Unmarshal([]byte(result.String), &j.Result); err != nil {
			return j, err
		}
	}
	if err = json.Unmarshal([]byte(ids), &j.AccountIDs); err != nil {
		return j, err
	}
	if j.AccountIDs == nil {
		j.AccountIDs = []string{}
	}
	return j, nil
}
func enqueueJob(ctx context.Context, tx *sql.Tx, kind, scope string, ids []string, now time.Time) (model.Job, error) {
	if kind != "refresh" && kind != "permission_check" && kind != "telegram_test" && kind != "heartbeat_test" {
		return model.Job{}, errors.New("unsupported_job_type")
	}
	j, err := readJob(tx.QueryRowContext(ctx, "SELECT "+jobColumns+" FROM job WHERE type=? AND scope=? AND status IN ('queued','running')", kind, scope))
	if err == nil {
		return j, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return j, err
	}
	if ids == nil {
		ids = []string{}
	}
	j = model.Job{ID: model.NewID("job_"), Type: kind, Status: "queued", CreatedAt: now, AccountIDs: ids, Trigger: model.JobTrigger(ctx)}
	raw, err := json.Marshal(ids)
	if err != nil {
		return j, err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO job(id,type,scope,account_ids_json,status,created_at,available_at,trigger_source) VALUES(?,?,?,?,?,?,?,?)", j.ID, kind, scope, string(raw), j.Status, timestamp(now), now.Unix(), j.Trigger)
	if err != nil {
		return j, err
	}
	return readJob(tx.QueryRowContext(ctx, "SELECT "+jobColumns+" FROM job WHERE id=?", j.ID))
}
func (s *Store) EnqueueJob(ctx context.Context, kind, scope string, ids []string, now time.Time) (model.Job, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return model.Job{}, err
	}
	defer tx.Rollback()
	for _, id := range ids {
		a, err := loadAccount(ctx, tx, id)
		if err != nil {
			return model.Job{}, err
		}
		if !a.Enabled {
			return model.Job{}, ErrAccountDisabled
		}
	}
	j, err := enqueueJob(ctx, tx, kind, scope, ids, now)
	if err != nil {
		return j, err
	}
	return j, tx.Commit()
}
func (s *Store) Job(ctx context.Context, id string) (model.Job, error) {
	return readJob(s.DB.QueryRowContext(ctx, "SELECT "+jobColumns+" FROM job WHERE id=?", id))
}

func (s *Store) Jobs(ctx context.Context, filter model.JobFilter, before int64, limit int) ([]model.Job, error) {
	if before < 0 || limit < 1 || limit > 201 || len(filter.Validate()) != 0 {
		return nil, ErrNotFound
	}
	clauses := []string{"1=1"}
	args := []any{}
	for _, field := range []struct{ column, value string }{{"job.type", filter.Type}, {"job.status", filter.Status}, {"job.trigger_source", filter.Trigger}, {"job.instance_id", filter.InstanceID}} {
		if field.value != "" {
			clauses = append(clauses, field.column+"=?")
			args = append(args, field.value)
		}
	}
	if filter.AccountID != "" {
		clauses = append(clauses, "EXISTS (SELECT 1 FROM json_each(job.account_ids_json) WHERE value=?)")
		args = append(args, filter.AccountID)
	}
	if before != 0 {
		clauses = append(clauses, "job_order.seq<?")
		args = append(args, before)
	}
	args = append(args, limit)
	rows, err := s.DB.QueryContext(ctx, "SELECT "+jobColumns+" FROM job JOIN job_order ON job_order.job_id=job.id WHERE "+strings.Join(clauses, " AND ")+" ORDER BY job_order.seq DESC LIMIT ?", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	jobs := []model.Job{}
	for rows.Next() {
		j, err := readJob(rows)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, j)
	}
	return jobs, rows.Err()
}

func (s *Store) JobChangeHead(ctx context.Context) (int64, error) {
	var seq int64
	err := s.DB.QueryRowContext(ctx, "SELECT coalesce(max(seq),0) FROM job_change").Scan(&seq)
	return seq, err
}
func (s *Store) JobChanges(ctx context.Context, after int64) ([]model.Job, int64, error) {
	rows, err := s.DB.QueryContext(ctx, "SELECT seq,job_id FROM job_change WHERE seq>? ORDER BY seq LIMIT 100", after)
	if err != nil {
		return nil, after, err
	}
	ids := []string{}
	seen := map[string]bool{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&after, &id); err != nil {
			rows.Close()
			return nil, after, err
		}
		if !seen[id] {
			ids = append(ids, id)
			seen[id] = true
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, after, err
	}
	jobs := []model.Job{}
	for _, id := range ids {
		j, err := s.Job(ctx, id)
		if err != nil {
			return nil, after, err
		}
		jobs = append(jobs, j)
	}
	return jobs, after, nil
}

// ClaimJob recovers expired read jobs. An owner nonce fences every later write;
// attempts are bounded even when the process repeatedly crashes mid-query.
func (s *Store) ClaimJob(ctx context.Context, now time.Time) (model.Job, error) {
	return s.ClaimCollectionJob(ctx, now)
}

// Collection workers must not consume Telegram test jobs: those jobs follow
// notification delivery leases and a separate uncertain-delivery policy.
func (s *Store) ClaimCollectionJob(ctx context.Context, now time.Time) (model.Job, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return model.Job{}, err
	}
	defer tx.Rollback()
	filter := " AND type IN ('refresh','permission_check')"
	if _, err = tx.ExecContext(ctx, `UPDATE job SET status='failed',error_code='retry_exhausted',finished_at=?,lease_owner=NULL,lease_until=0
 WHERE attempts>=3 AND ((status='running' AND lease_until<=?) OR status='queued')`+filter, timestamp(now), now.Unix()); err != nil {
		return model.Job{}, err
	}
	j, err := readJob(tx.QueryRowContext(ctx, "SELECT "+jobColumns+" FROM job WHERE ((status='queued' AND available_at<=?) OR (status='running' AND lease_until<=?))"+filter+" ORDER BY created_at,id LIMIT 1", now.Unix(), now.Unix()))
	if errors.Is(err, ErrNotFound) {
		if e := tx.Commit(); e != nil {
			return j, e
		}
		return j, ErrNotFound
	}
	if err != nil {
		return j, err
	}
	j.Owner = model.NewID("lease_")
	j.Status = "running"
	j.Attempts++
	if j.StartedAt == nil {
		t := now
		j.StartedAt = &t
	}
	_, err = tx.ExecContext(ctx, "UPDATE job SET status='running',started_at=coalesce(started_at,?),lease_owner=?,lease_until=?,attempts=attempts+1 WHERE id=?", timestamp(now), j.Owner, now.Add(time.Minute).Unix(), j.ID)
	if err != nil {
		return j, err
	}
	return j, tx.Commit()
}
func (s *Store) RenewJob(ctx context.Context, j model.Job, now time.Time) error {
	res, err := s.DB.ExecContext(ctx, "UPDATE job SET lease_until=? WHERE id=? AND status='running' AND lease_owner=? AND lease_until>?", now.Add(time.Minute).Unix(), j.ID, j.Owner, now.Unix())
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return ErrLeaseLost
	}
	return nil
}
func (s *Store) FinishJob(ctx context.Context, j model.Job, result any, code string, now time.Time) error {
	status := "succeeded"
	var failure any
	if code != "" {
		status = "failed"
		failure = code
	}
	var raw any
	if result != nil {
		b, err := json.Marshal(result)
		if err != nil {
			return err
		}
		raw = string(b)
	}
	res, err := s.DB.ExecContext(ctx, "UPDATE job SET status=?,finished_at=?,error_code=?,result_json=?,lease_owner=NULL,lease_until=0 WHERE id=? AND type NOT IN ('instance_power','heartbeat_test') AND status='running' AND lease_owner=? AND lease_until>?", status, timestamp(now), failure, raw, j.ID, j.Owner, now.Unix())
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return ErrLeaseLost
	}
	return nil
}
func (s *Store) RetryJob(ctx context.Context, j model.Job, delay time.Duration, busy bool, now time.Time) error {
	decrement := 0
	if busy {
		decrement = 1
	}
	res, err := s.DB.ExecContext(ctx, "UPDATE job SET status='queued',available_at=?,lease_owner=NULL,lease_until=0,attempts=attempts-? WHERE id=? AND type NOT IN ('instance_power','heartbeat_test') AND status='running' AND lease_owner=? AND lease_until>?", now.Add(delay).Unix(), decrement, j.ID, j.Owner, now.Unix())
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return ErrLeaseLost
	}
	return nil
}
func (s *Store) ScheduleScans(ctx context.Context, interval time.Duration, now time.Time) error {
	ctx = model.WithJobTrigger(ctx, "schedule")
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, "SELECT account_id FROM account_settings WHERE enabled=1 AND next_scan_at<=? ORDER BY account_id", now.Unix())
	if err != nil {
		return err
	}
	ids := []string{}
	for rows.Next() {
		var id string
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
		if _, err = enqueueJob(ctx, tx, "refresh", "inventory:"+id, []string{id}, now); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "UPDATE account_settings SET next_scan_at=? WHERE account_id=?", now.Add(interval).Unix(), id); err != nil {
			return err
		}
	}
	return tx.Commit()
}
