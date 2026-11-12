package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/netip"
	"sort"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
	"github.com/JasperXzy/harbormaster/internal/netprobe"
)

const probeAddressFreshness = 10 * time.Minute

func saveProbeAddresses(ctx context.Context, tx *sql.Tx, a model.Account, snapshot model.Snapshot, now time.Time) error {
	values, err := eventValues(snapshot)
	if err != nil {
		return err
	}
	var v4, v6 any
	if values.PublicIPv4 != nil {
		raw, _ := json.Marshal(values.PublicIPv4)
		v4 = string(raw)
	}
	if values.IPv6 != nil {
		raw, _ := json.Marshal(values.IPv6)
		v6 = string(raw)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO instance_probe_address(instance_id,credential_version,observed_at,ipv4_json,ipv6_json) VALUES(?,?,?,?,?) ON CONFLICT(instance_id) DO UPDATE SET credential_version=excluded.credential_version,observed_at=excluded.observed_at,ipv4_json=excluded.ipv4_json,ipv6_json=excluded.ipv6_json`, snapshot.ID, a.CredentialVersion, timestamp(now), v4, v6)
	return err
}
func probePoint(ctx context.Context, q queryRower, id string, now time.Time) (model.ProbePoint, error) {
	if id == "server" {
		return model.ProbePoint{ID: id, Name: "hm-server", PointKind: "server", Available: true}, nil
	}
	v := model.ProbePoint{ID: id, PointKind: "agent", InstanceID: &id}
	var agent, revoked, received, observed, polled sql.NullString
	var enabled, present bool
	var version, current sql.NullInt64
	var seconds int
	err := q.QueryRowContext(ctx, `SELECT i.name,g.id,g.revoked_at,g.last_received_at,a.enabled,EXISTS(SELECT 1 FROM resource_snapshot r WHERE r.id=i.id AND r.account_id=i.account_id AND r.kind=i.kind AND r.present=1),c.credential_version,a.credential_version,c.observed_at,contact.last_poll_at,coalesce((SELECT json_extract(params_json,'$.offline_after_seconds') FROM alert_rule WHERE key='agent_offline'),30)
 FROM instance i JOIN account_settings a ON a.account_id=i.account_id LEFT JOIN agent g ON g.instance_id=i.id LEFT JOIN instance_probe_address c ON c.instance_id=i.id LEFT JOIN agent_probe_contact contact ON contact.agent_id=g.id AND contact.token_hash=g.token_hash WHERE i.id=?`, id).Scan(&v.Name, &agent, &revoked, &received, &enabled, &present, &version, &current, &observed, &polled, &seconds)
	if errors.Is(err, sql.ErrNoRows) {
		return v, ErrNotFound
	}
	if err != nil {
		return v, err
	}
	v.UpdatedAt = parseAgentTime(received)
	probeAt := parseAgentTime(polled)
	reason := ""
	switch {
	case !enabled:
		reason = "account_disabled"
	case !present:
		reason = "instance_unavailable"
	case !agent.Valid:
		reason = "agent_not_registered"
	case revoked.Valid:
		reason = "agent_revoked"
	case !version.Valid || version.Int64 != current.Int64:
		reason = "point_discovery_unverified"
	case v.UpdatedAt == nil || v.UpdatedAt.After(now.Add(30*time.Second)) || !now.Before(v.UpdatedAt.Add(time.Duration(seconds)*time.Second)):
		reason = "agent_offline"
	case probeAt == nil || probeAt.After(now.Add(30*time.Second)) || !now.Before(probeAt.Add(5*time.Minute)):
		reason = "agent_probe_unavailable"
	}
	if reason != "" {
		v.UnavailableReason = &reason
	} else {
		v.Available = true
	}
	return v, nil
}
func (s *Store) ProbePoints(ctx context.Context, after string, limit int, now time.Time) ([]model.ProbePoint, error) {
	if limit < 1 || limit > 201 {
		return nil, netprobe.ErrInvalid
	}
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, "SELECT id FROM instance WHERE id>? UNION SELECT 'server' WHERE 'server'>? ORDER BY id LIMIT ?", after, after, limit)
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
	out := []model.ProbePoint{}
	for _, id := range ids {
		v, err := probePoint(ctx, tx, id, now)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, tx.Commit()
}
func resolveProbeTarget(ctx context.Context, q queryRower, target model.NetworkTarget, family string, now time.Time) (string, string, error) {
	if !target.Enabled {
		return "", "target_disabled", nil
	}
	if target.Kind == "custom" {
		if target.Host == nil {
			return "", "target_invalid", nil
		}
		return *target.Host, "", nil
	}
	if target.InstanceID == nil {
		return "", "target_invalid", nil
	}
	var enabled, present bool
	var version, current sql.NullInt64
	var at, v4, v6 sql.NullString
	err := q.QueryRowContext(ctx, `SELECT a.enabled,EXISTS(SELECT 1 FROM resource_snapshot r WHERE r.id=i.id AND r.account_id=i.account_id AND r.kind=i.kind AND r.present=1),c.credential_version,a.credential_version,c.observed_at,c.ipv4_json,c.ipv6_json FROM instance i JOIN account_settings a ON a.account_id=i.account_id LEFT JOIN instance_probe_address c ON c.instance_id=i.id WHERE i.id=?`, *target.InstanceID).Scan(&enabled, &present, &version, &current, &at, &v4, &v6)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "target_instance_unavailable", nil
	}
	if err != nil {
		return "", "", err
	}
	if !enabled || !present {
		return "", "target_instance_unavailable", nil
	}
	raw := v4
	if family == "ipv6" {
		raw = v6
	}
	if !version.Valid || version.Int64 != current.Int64 || !raw.Valid {
		return "", "target_discovery_incomplete", nil
	}
	observed := parseAgentTime(at)
	if observed == nil || observed.After(now) || now.Sub(*observed) > probeAddressFreshness {
		return "", "target_discovery_stale", nil
	}
	var addresses []string
	if json.Unmarshal([]byte(raw.String), &addresses) != nil {
		return "", "target_discovery_incomplete", nil
	}
	found := map[string]bool{}
	for _, address := range addresses {
		ip, err := netip.ParseAddr(address)
		if err != nil || ip.Is4() != (family == "ipv4") {
			return "", "target_discovery_incomplete", nil
		}
		if netprobe.PublicAddress(ip) {
			found[ip.String()] = true
		}
	}
	if len(found) == 0 {
		return "", "target_no_public_address", nil
	}
	if len(found) > 1 {
		return "", "target_ambiguous_address", nil
	}
	sorted := []string{}
	for ip := range found {
		sorted = append(sorted, ip)
	}
	sort.Strings(sorted)
	return sorted[0], "", nil
}

// Recheck following-instance targets before either worker claims a queued run.
// Its actual enqueue address remains in history when discovery invalidates it.
func validateQueuedProbeTargets(ctx context.Context, tx *sql.Tx, now time.Time) error {
	rows, err := tx.QueryContext(ctx, `SELECT p.job_id,r.target_id,p.request_json FROM network_probe_job p JOIN job j ON j.id=p.job_id JOIN probe_plan_run r ON r.job_id=j.id JOIN network_target t ON t.id=r.target_id WHERE j.status='queued' AND json_extract(t.config_json,'$.kind')='instance'`)
	if err != nil {
		return err
	}
	type pending struct{ id, target, request string }
	items := []pending{}
	for rows.Next() {
		var item pending
		if err = rows.Scan(&item.id, &item.target, &item.request); err != nil {
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
		target, err := readTarget(ctx, tx, item.target)
		if err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}
		var request netprobe.Request
		if json.Unmarshal([]byte(item.request), &request) != nil {
			return netprobe.ErrInvalid
		}
		host, reason := "", "target_unavailable"
		if err == nil {
			host, reason, err = resolveProbeTarget(ctx, tx, target, request.Family, now)
			if err != nil {
				return err
			}
		}
		if reason != "" || host != request.Host {
			if _, err = tx.ExecContext(ctx, "UPDATE job SET status='failed',error_code='probe_target_changed',finished_at=?,lease_owner=NULL,lease_until=0 WHERE id=? AND status='queued'", timestamp(now), item.id); err != nil {
				return err
			}
		}
	}
	return nil
}
