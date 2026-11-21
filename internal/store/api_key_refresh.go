package store

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
)

var ErrRefreshScopeChanged = errors.New("refresh_scope_changed")
var ErrRefreshParameters = errors.New("invalid_refresh_parameters")

// EnqueueAPIKeyRefresh joins a read-only collector job and records visibility
// atomically. Enabled monitoring authorizes collection independently of the key;
// revoking one participant must not cancel work that also serves other callers.
func (s *Store) EnqueueAPIKeyRefresh(ctx context.Context, keyID, accountID string, now time.Time) (model.Job, error) {
	if len(accountID) > 128 {
		return model.Job{}, ErrRefreshParameters
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return model.Job{}, err
	}
	defer tx.Rollback()
	if err = requireControlAPIKey(ctx, tx, keyID); err != nil {
		return model.Job{}, err
	}
	ids := []string{}
	scope := accountID
	if accountID != "" {
		a, err := loadAccount(ctx, tx, accountID)
		if err != nil {
			return model.Job{}, err
		}
		if !a.Enabled {
			return model.Job{}, ErrAccountDisabled
		}
		ids = append(ids, accountID)
	} else {
		scope = "all"
		rows, err := tx.QueryContext(ctx, "SELECT account_id FROM account_settings WHERE enabled=1 ORDER BY account_id")
		if err != nil {
			return model.Job{}, err
		}
		for rows.Next() {
			var id string
			if err = rows.Scan(&id); err != nil {
				rows.Close()
				return model.Job{}, err
			}
			ids = append(ids, id)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return model.Job{}, err
		}
	}
	j, err := enqueueJob(model.WithJobTrigger(ctx, "api"), tx, "refresh", scope, ids, now)
	if err != nil {
		return j, err
	}
	// Existing all-account work may have captured a different membership. Do not
	// claim it covers this request or modify the account set of a running job.
	if !slices.Equal(j.AccountIDs, ids) {
		return model.Job{}, ErrRefreshScopeChanged
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO api_key_refresh_job(job_id,key_id,joined_at) VALUES(?,?,?) ON CONFLICT(job_id,key_id) DO NOTHING", j.ID, keyID, timestamp(now)); err != nil {
		return model.Job{}, err
	}
	return j, tx.Commit()
}

// APIKeyControlJob exposes only explicitly authorized memberships. A read GET
// cannot join work just because its scope happens to match an account.
func (s *Store) APIKeyControlJob(ctx context.Context, keyID, id string) (model.Job, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return model.Job{}, err
	}
	defer tx.Rollback()
	if err = requireControlAPIKey(ctx, tx, keyID); err != nil {
		return model.Job{}, err
	}
	j, err := readJob(tx.QueryRowContext(ctx, "SELECT "+jobColumns+` FROM job WHERE id=? AND (
 (type='instance_power' AND EXISTS(SELECT 1 FROM api_key_power_job k WHERE k.job_id=job.id AND k.key_id=?)) OR
 (type='refresh' AND EXISTS(SELECT 1 FROM api_key_refresh_job k WHERE k.job_id=job.id AND k.key_id=?)))`, id, keyID, keyID))
	if err != nil {
		return j, err
	}
	return j, tx.Commit()
}

// EnqueueCronRun implements PRD F13.4 URL Cron compatibility: authenticates a
// cron:run API key, enqueues a refresh job if enabled accounts exist, and logs audit.
func (s *Store) EnqueueCronRun(ctx context.Context, keyID, accountID string, now time.Time) (*model.Job, error) {
	if len(accountID) > 128 {
		return nil, ErrRefreshParameters
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err = requireAPIKeyScope(ctx, tx, keyID, "cron:run"); err != nil {
		return nil, err
	}
	ids := []string{}
	scope := accountID
	if accountID != "" {
		a, err := loadAccount(ctx, tx, accountID)
		if err != nil {
			return nil, err
		}
		if !a.Enabled {
			return nil, ErrAccountDisabled
		}
		ids = append(ids, accountID)
	} else {
		scope = "all"
		rows, err := tx.QueryContext(ctx, "SELECT account_id FROM account_settings WHERE enabled=1 ORDER BY account_id")
		if err != nil {
			return nil, err
		}
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
	}
	var out *model.Job
	if len(ids) > 0 {
		j, err := enqueueJob(model.WithJobTrigger(ctx, "api"), tx, "refresh", scope, ids, now)
		if err != nil {
			return nil, err
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO api_key_refresh_job(job_id,key_id,joined_at) VALUES(?,?,?) ON CONFLICT(job_id,key_id) DO NOTHING", j.ID, keyID, timestamp(now)); err != nil {
			return nil, err
		}
		out = &j
	}
	auditCtx := model.WithAPIKeyPrincipal(ctx, keyID)
	var jobID string
	if out != nil {
		jobID = out.ID
	}
	if err = appendAudit(auditCtx, tx, auditEvent{
		Action:     "cron_triggered",
		TargetType: "system",
		TargetID:   scope,
		JobID:      jobID,
		Details:    model.AuditDetails{ChangedFields: []string{"scope"}, After: map[string]any{"scope": scope, "account_count": len(ids)}},
	}, now); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return out, nil
}
