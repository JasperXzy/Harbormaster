package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"sort"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
)

type dailySubject struct {
	meteringSubject
	Kind      string   `json:"kind"`
	PublicIPs []string `json:"public_ips"`
}

// An empty attached-address list is evidence only for an otherwise complete
// discovery of an empty bandwidth package. A missing list is not an empty list.
func (v dailySubject) knownEmpty() bool {
	return v.Kind == "cbwp_dominant_traffic" && v.PublicIPs != nil && len(v.PublicIPs) == 0 && len(v.Members) == 0
}

func dailyGraph(raw []map[string]any) (string, string, bool, error) {
	subs := []dailySubject{}
	empty := false
	for _, object := range raw {
		b, err := json.Marshal(object)
		if err != nil {
			return "", "", false, err
		}
		var sub dailySubject
		if err = json.Unmarshal(b, &sub); err != nil {
			return "", "", false, err
		}
		sort.Strings(sub.Members)
		sort.Strings(sub.PublicIPs)
		if (sub.Eligible == nil || *sub.Eligible) && sub.knownEmpty() {
			empty = true
		}
		subs = append(subs, sub)
	}
	sort.Slice(subs, func(i, j int) bool { return subs[i].ID < subs[j].ID })
	b, err := json.Marshal(subs)
	sum := sha256.Sum256(b)
	return string(b), hex.EncodeToString(sum[:]), empty, err
}

func saveDailyTopology(ctx context.Context, tx *sql.Tx, a model.Account, region string, objects []map[string]any, complete bool, since, now time.Time) error {
	raw, fingerprint, empty, err := dailyGraph(objects)
	if err != nil {
		return err
	}
	var id, credential int64
	var oldHash, oldSeen string
	err = tx.QueryRowContext(ctx, "SELECT id,credential_version,fingerprint,last_seen FROM daily_topology WHERE account_id=? AND region=? AND closed_at IS NULL", a.ID, region).Scan(&id, &credential, &oldHash, &oldSeen)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	last, _ := time.Parse(time.RFC3339Nano, oldSeen)
	if err == nil && complete && credential == a.CredentialVersion && fingerprint == oldHash && !last.After(now) && now.Sub(last) <= meteringTopologyFreshness {
		_, err = tx.ExecContext(ctx, "UPDATE daily_topology SET last_seen=? WHERE id=?", timestamp(now), id)
		return err
	}
	if id != 0 {
		if _, err = tx.ExecContext(ctx, "UPDATE daily_topology SET closed_at=last_seen WHERE id=?", id); err != nil {
			return err
		}
	}
	if complete {
		// Legacy fingerprints did not include addresses: do not backdate proof of emptiness.
		if id != 0 || empty || since.IsZero() || since.After(now) {
			since = now
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO daily_topology(account_id,region,credential_version,fingerprint,observed_from,last_seen,subjects_json) VALUES(?,?,?,?,?,?,?)`, a.ID, region, a.CredentialVersion, fingerprint, timestamp(since), timestamp(now), raw); err != nil {
			return err
		}
	}
	changedFrom := since
	if id != 0 {
		changedFrom = last
	}
	if changedFrom.IsZero() {
		changedFrom = now
	}
	// One preceding UTC date also covers business zones on either side of UTC.
	_, err = tx.ExecContext(ctx, "UPDATE daily_work SET generation=generation+1,pending=1 WHERE account_id=? AND day>=?", a.ID, changedFrom.AddDate(0, 0, -1).UTC().Format("2006-01-02"))
	return err
}

// Seed only the current stable legacy graph; older graphs were never recorded.
func seedDailyTopology(ctx context.Context, tx *sql.Tx, a model.Account) error {
	type seed struct {
		region      string
		since, seen time.Time
	}
	rows, err := tx.QueryContext(ctx, `SELECT t.region,t.observed_since,t.last_seen FROM metering_topology t WHERE t.account_id=? AND t.credential_version=? AND t.complete=1 AND NOT EXISTS(SELECT 1 FROM daily_topology d WHERE d.account_id=t.account_id AND d.region=t.region)`, a.ID, a.CredentialVersion)
	if err != nil {
		return err
	}
	seeds := []seed{}
	for rows.Next() {
		var v seed
		var from, to string
		if err = rows.Scan(&v.region, &from, &to); err != nil {
			rows.Close()
			return err
		}
		v.since, _ = time.Parse(time.RFC3339Nano, from)
		v.seen, _ = time.Parse(time.RFC3339Nano, to)
		if slices.Contains(a.RegionIDs, v.region) {
			seeds = append(seeds, v)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, v := range seeds {
		r, err := tx.QueryContext(ctx, "SELECT payload_json FROM resource_snapshot WHERE account_id=? AND region=? AND kind='subject' AND present=1 ORDER BY id", a.ID, v.region)
		if err != nil {
			return err
		}
		objects := []map[string]any{}
		for r.Next() {
			var raw string
			var object map[string]any
			if err = r.Scan(&raw); err == nil {
				err = json.Unmarshal([]byte(raw), &object)
			}
			if err != nil {
				r.Close()
				return err
			}
			objects = append(objects, object)
		}
		err = r.Err()
		r.Close()
		if err != nil {
			return err
		}
		if err = saveDailyTopology(ctx, tx, a, v.region, objects, true, v.since, v.seen); err != nil {
			return err
		}
	}
	return nil
}
