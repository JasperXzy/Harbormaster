package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/JasperXzy/harbormaster/internal/model"
)

// retainIPv6 runs inside the fenced snapshot transaction. A failed refresh may
// update attempt/error metadata, but never erase the previous successful result.
func retainIPv6(ctx context.Context, tx *sql.Tx, accountID string, snapshot model.Snapshot) error {
	view, ok := snapshot.Payload["ipv6"].(map[string]any)
	if snapshot.Kind != "ecs" || !ok || view["status"] != "unknown" {
		return nil
	}
	var raw string
	err := tx.QueryRowContext(ctx, "SELECT payload_json FROM resource_snapshot WHERE account_id=? AND kind='ecs' AND id=? AND region=?", accountID, snapshot.ID, snapshot.Region).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var previous map[string]any
	if err := json.Unmarshal([]byte(raw), &previous); err != nil {
		return err
	}
	old, ok := previous["ipv6"].(map[string]any)
	if !ok || (old["status"] != "ok" && old["status"] != "stale") || old["observed_at"] == nil {
		return nil
	}
	for _, key := range []string{"addresses", "source", "observed_at"} {
		view[key] = old[key]
	}
	view["status"] = "stale"
	return nil
}
