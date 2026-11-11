package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"errors"
	"time"

	"github.com/JasperXzy/harbormaster/internal/agentproto"
	"github.com/JasperXzy/harbormaster/internal/model"
)

var ErrRegistration = errors.New("invalid_registration_code")
var ErrIdentity = errors.New("instance_identity_mismatch")
var ErrRegistrationConsumed = errors.New("registration_consumed")
var ErrAgentUnsupported = errors.New("agent_not_supported")

// A grant never revokes the current token. Rotation occurs only when a new
// agent successfully completes registration against this prebound instance.
func (s *Store) AgentGrant(ctx context.Context, instanceID string, now time.Time) (string, time.Time, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return "", time.Time{}, err
	}
	defer tx.Rollback()
	var enabled, present bool
	err = tx.QueryRowContext(ctx, `SELECT a.enabled,EXISTS(SELECT 1 FROM resource_snapshot r WHERE r.id=i.id AND r.account_id=i.account_id AND r.kind=i.kind AND r.present=1)
 FROM instance i JOIN account_settings a ON a.account_id=i.account_id
 WHERE i.id=? AND i.kind IN ('ecs','swas')`, instanceID).Scan(&enabled, &present)
	if errors.Is(err, sql.ErrNoRows) {
		return "", time.Time{}, ErrNotFound
	}
	if err != nil {
		return "", time.Time{}, err
	}
	if !enabled {
		return "", time.Time{}, ErrAccountDisabled
	}
	if !present {
		return "", time.Time{}, ErrAgentUnsupported
	}
	var raw [32]byte
	_, _ = rand.Read(raw[:])
	code := "hm_reg_" + hex.EncodeToString(raw[:])
	sum := sha256.Sum256([]byte(code))
	expiry := now.UTC().Add(10 * time.Minute)
	_, err = tx.ExecContext(ctx, `INSERT INTO agent_registration(instance_id,code_hash,expires_at) VALUES(?,?,?)
 ON CONFLICT(instance_id) DO UPDATE SET code_hash=excluded.code_hash,expires_at=excluded.expires_at,consumed_at=NULL,nonce_hash=NULL,agent_id=NULL`, instanceID, sum[:], timestamp(expiry))
	if err != nil {
		return "", time.Time{}, err
	}
	return code, expiry, tx.Commit()
}

func (s *Store) RegisterAgent(ctx context.Context, r agentproto.Registration, now time.Time) (agentproto.Registered, error) {
	if r.Validate() != nil {
		return agentproto.Registered{}, ErrRegistration
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return agentproto.Registered{}, err
	}
	defer tx.Rollback()
	codeHash := sha256.Sum256([]byte(r.Code))
	nonceHash := sha256.Sum256([]byte(r.Nonce))
	tokenHash := sha256.Sum256([]byte(agentproto.DeriveToken(r.Code, r.Nonce)))
	var instance, kind, cloudID, region, expires string
	var consumed, agentID sql.NullString
	var usedNonce []byte
	var enabled, present bool
	err = tx.QueryRowContext(ctx, `SELECT g.instance_id,i.kind,i.cloud_id,i.region,g.expires_at,g.consumed_at,g.nonce_hash,g.agent_id,a.enabled,
 EXISTS(SELECT 1 FROM resource_snapshot rs WHERE rs.account_id=i.account_id AND rs.id=i.id AND rs.kind=i.kind AND rs.present=1)
 FROM agent_registration g JOIN instance i ON i.id=g.instance_id JOIN account_settings a ON a.account_id=i.account_id WHERE g.code_hash=?`, codeHash[:]).Scan(&instance, &kind, &cloudID, &region, &expires, &consumed, &usedNonce, &agentID, &enabled, &present)
	if errors.Is(err, sql.ErrNoRows) {
		return agentproto.Registered{}, ErrRegistration
	}
	if err != nil {
		return agentproto.Registered{}, err
	}
	expiry, err := time.Parse(time.RFC3339Nano, expires)
	if err != nil || !now.Before(expiry) {
		return agentproto.Registered{}, ErrRegistration
	}
	if !enabled || !present {
		return agentproto.Registered{}, ErrAccountDisabled
	}
	if region != r.Metadata.RegionID || (kind == "ecs" && cloudID != r.Metadata.InstanceID) {
		return agentproto.Registered{}, ErrIdentity
	}
	response := agentproto.Registered{TokenScheme: "hmac-sha256-v1", ReportIntervalSeconds: 10, ServerTime: now.UTC()}
	if consumed.Valid {
		if subtle.ConstantTimeCompare(usedNonce, nonceHash[:]) != 1 {
			return response, ErrRegistrationConsumed
		}
		var valid int
		if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM agent WHERE id=? AND instance_id=? AND token_hash=? AND revoked_at IS NULL", agentID.String, instance, tokenHash[:]).Scan(&valid); err != nil {
			return response, err
		}
		if valid != 1 {
			return response, ErrRegistrationConsumed
		}
		response.AgentID = agentID.String
		return response, nil
	}
	var id string
	err = tx.QueryRowContext(ctx, "SELECT id FROM agent WHERE instance_id=?", instance).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		id = model.NewID("agent_")
	} else if err != nil {
		return response, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO agent(id,instance_id,token_hash,version,registered_at,metadata_instance_id,metadata_region_id) VALUES(?,?,?,?,?,?,?)
 ON CONFLICT(instance_id) DO UPDATE SET token_hash=excluded.token_hash,version=excluded.version,registered_at=excluded.registered_at,
 metadata_instance_id=excluded.metadata_instance_id,metadata_region_id=excluded.metadata_region_id,revoked_at=NULL,last_received_at=NULL`, id, instance, tokenHash[:], r.AgentVersion, timestamp(now), r.Metadata.InstanceID, r.Metadata.RegionID)
	if err != nil {
		return response, err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE agent_registration SET consumed_at=?,nonce_hash=?,agent_id=? WHERE instance_id=?", timestamp(now), nonceHash[:], id, instance); err != nil {
		return response, err
	}
	response.AgentID = id
	return response, tx.Commit()
}
func (s *Store) RevokeAgent(ctx context.Context, instanceID string, now time.Time) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var count int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM instance WHERE id=?", instanceID).Scan(&count); err != nil {
		return err
	}
	if count == 0 {
		return ErrNotFound
	}
	if _, err = tx.ExecContext(ctx, "UPDATE agent SET revoked_at=?,last_received_at=NULL WHERE instance_id=?", timestamp(now), instanceID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM agent_registration WHERE instance_id=?", instanceID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE alert_state SET active=0,changed_at=? WHERE rule_key='agent_offline' AND target_id=? AND active=1", timestamp(now), instanceID); err != nil {
		return err
	}
	if err = cancelPendingNotifications(ctx, tx, "rule_key='agent_offline' AND instance_id=?", []any{instanceID}, "agent_revoked", now); err != nil {
		return err
	}
	return tx.Commit()
}
