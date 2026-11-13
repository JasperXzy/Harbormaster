package store

import (
	"context"
	"errors"

	"github.com/JasperXzy/harbormaster/internal/model"
)

func requireControlAPIKey(ctx context.Context, q queryRower, id string) error {
	key, err := readAPIKey(q.QueryRowContext(ctx, "SELECT "+keyColumns+" FROM api_key WHERE id=? AND revoked_at IS NULL", id))
	if errors.Is(err, ErrNotFound) {
		return ErrUnauthorized
	}
	if err != nil {
		return err
	}
	for _, scope := range key.Scopes {
		if scope == "instance:control" {
			return nil
		}
	}
	return ErrAPIKeyScope
}

func (s *Store) APIKeyPowerJob(ctx context.Context, keyID, id string) (model.Job, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return model.Job{}, err
	}
	defer tx.Rollback()
	if err = requireControlAPIKey(ctx, tx, keyID); err != nil {
		return model.Job{}, err
	}
	j, err := readJob(tx.QueryRowContext(ctx, "SELECT "+jobColumns+" FROM job WHERE id=? AND type='instance_power' AND EXISTS (SELECT 1 FROM api_key_power_job k WHERE k.job_id=job.id AND k.key_id=?)", id, keyID))
	if err != nil {
		return j, err
	}
	return j, tx.Commit()
}
