package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
)

var ErrEstimationSettings = errors.New("invalid_estimation_settings")
var ErrEstimationRevision = errors.New("revision_conflict")

func readEstimationSettings(ctx context.Context, q queryRower) (model.EstimationSettings, error) {
	var v model.EstimationSettings
	var raw string
	err := q.QueryRowContext(ctx, "SELECT revision,lag_seconds,bandwidth_ids_json FROM metering_settings WHERE id=1").Scan(&v.Revision, &v.LagSeconds, &raw)
	if err == nil {
		err = json.Unmarshal([]byte(raw), &v.BandwidthIDs)
	}
	return v, err
}
func (s *Store) EstimationSettings(ctx context.Context) (model.EstimationSettings, error) {
	return readEstimationSettings(ctx, s.DB)
}
func (s *Store) SaveEstimationSettings(ctx context.Context, v model.EstimationSettings) (model.EstimationSettings, error) {
	if v.Revision < 0 || v.LagSeconds < 60 || v.LagSeconds > 604800 || v.BandwidthIDs == nil || len(v.BandwidthIDs) > 1000 {
		return v, ErrEstimationSettings
	}
	slices.Sort(v.BandwidthIDs)
	for i, id := range v.BandwidthIDs {
		if id == "" || i > 0 && id == v.BandwidthIDs[i-1] {
			return v, ErrEstimationSettings
		}
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return v, err
	}
	defer tx.Rollback()
	current, err := readEstimationSettings(ctx, tx)
	if err != nil {
		return v, err
	}
	if current.Revision != v.Revision {
		return v, ErrEstimationRevision
	}
	for _, id := range v.BandwidthIDs {
		var n int
		if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM instance WHERE id=?", id).Scan(&n); err != nil {
			return v, err
		}
		if n != 1 {
			return v, ErrEstimationSettings
		}
	}
	raw, err := json.Marshal(v.BandwidthIDs)
	if err != nil {
		return v, err
	}
	if current.LagSeconds == v.LagSeconds && slices.Equal(current.BandwidthIDs, v.BandwidthIDs) {
		return current, tx.Commit()
	}
	v.Revision++
	if _, err = tx.ExecContext(ctx, "UPDATE metering_settings SET revision=?,lag_seconds=?,bandwidth_ids_json=? WHERE id=1", v.Revision, v.LagSeconds, string(raw)); err != nil {
		return v, err
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM traffic_estimate"); err != nil {
		return v, err
	}
	details := auditDiff(auditChange{"lag_window_seconds", current.LagSeconds, v.LagSeconds}, auditChange{"bandwidth_mode_instance_ids", current.BandwidthIDs, v.BandwidthIDs})
	details.Revision = &v.Revision
	if err = appendAudit(ctx, tx, auditEvent{Action: "estimation_settings_updated", TargetType: "settings", TargetID: "metering", Details: details}, time.Now().UTC()); err != nil {
		return v, err
	}
	return v, tx.Commit()
}
func nullableMeteringTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return timestamp(*t)
}
func (s *Store) cachedEstimate(ctx context.Context, a model.Account, scope, basis string, now time.Time) (*model.TrafficEstimation, error) {
	if !a.Enabled {
		return nil, nil
	}
	var raw string
	err := s.DB.QueryRowContext(ctx, `SELECT e.payload_json FROM traffic_estimate e JOIN metering_settings m ON m.id=1
 WHERE e.account_id=? AND e.scope=? AND e.basis_id=? AND e.account_revision=? AND e.credential_version=? AND e.settings_revision=m.revision`, a.ID, scope, basis, a.Revision, a.CredentialVersion).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var v model.TrafficEstimation
	if err = json.Unmarshal([]byte(raw), &v); err != nil {
		return nil, err
	}
	if now.Before(v.CalculatedAt) || now.Sub(v.CalculatedAt) > 30*time.Second {
		v.Status = "stale"
		v.Qualified = false
		v.Reasons = append(v.Reasons, "estimator_stale")
	}
	return &v, nil
}
