package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/JasperXzy/harbormaster/internal/model"
	"sort"
	"strings"
	"time"
)

const meteringTopologyFreshness = 15 * time.Minute

// Only complete discovery participates. Unknown attachment history before the
// first stable observation is not retroactively treated as known membership.
func saveMeteringTopology(ctx context.Context, tx *sql.Tx, a model.Account, result model.ScanResult, now time.Time) error {
	for key, complete := range result.CompleteScopes {
		if !strings.HasPrefix(key, "subject:") {
			continue
		}
		region := strings.TrimPrefix(key, "subject:")
		objects := []map[string]any{}
		for _, v := range result.Snapshots {
			if v.Kind == "subject" && v.Region == region {
				objects = append(objects, map[string]any{"id": v.ID, "rule": v.Payload["metering_rule"], "members": v.Payload["instance_ids"], "eligible": v.Payload["counts_toward_cdt"], "scope": v.Payload["cdt_scope"], "bandwidth": v.Payload["bandwidth_mbps"]})
			}
		}
		// Canonical ordering includes unordered member sets.
		for _, o := range objects {
			b, _ := json.Marshal(o["members"])
			var ids []string
			if json.Unmarshal(b, &ids) == nil {
				sort.Strings(ids)
				o["members"] = ids
			}
		}
		sort.Slice(objects, func(i, j int) bool { return objects[i]["id"].(string) < objects[j]["id"].(string) })
		raw, err := json.Marshal(objects)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(raw)
		fingerprint := hex.EncodeToString(sum[:])
		since := timestamp(now)
		var oldHash, oldSince, last string
		var oldComplete bool
		var credential int64
		err = tx.QueryRowContext(ctx, "SELECT fingerprint,observed_since,last_seen,complete,credential_version FROM metering_topology WHERE account_id=? AND region=?", a.ID, region).Scan(&oldHash, &oldSince, &last, &oldComplete, &credential)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		lastAt, _ := time.Parse(time.RFC3339Nano, last)
		if err == nil && complete && oldComplete && fingerprint == oldHash && credential == a.CredentialVersion && !lastAt.After(now) && now.Sub(lastAt) <= meteringTopologyFreshness {
			since = oldSince
		}
		full := []map[string]any{}
		for _, v := range result.Snapshots {
			if v.Kind == "subject" && v.Region == region {
				full = append(full, v.Payload)
			}
		}
		from, _ := time.Parse(time.RFC3339Nano, since)
		if err = saveDailyTopology(ctx, tx, a, region, full, complete, from, now); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO metering_topology(account_id,region,credential_version,fingerprint,observed_since,last_seen,complete) VALUES(?,?,?,?,?,?,?)
 ON CONFLICT(account_id,region) DO UPDATE SET credential_version=excluded.credential_version,fingerprint=excluded.fingerprint,observed_since=excluded.observed_since,last_seen=excluded.last_seen,complete=excluded.complete,quality='observed'`, a.ID, region, a.CredentialVersion, fingerprint, since, timestamp(now), complete)
		if err != nil {
			return err
		}
		if oldHash != fingerprint || !complete {
			if _, err = tx.ExecContext(ctx, "DELETE FROM traffic_estimate WHERE account_id=?", a.ID); err != nil {
				return err
			}
		}
	}
	return nil
}
