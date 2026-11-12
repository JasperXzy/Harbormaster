package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/JasperXzy/harbormaster/internal/model"
)

func retainNetworkRates(ctx context.Context, tx *sql.Tx, accountID string, snapshot model.Snapshot) error {
	current, ok := snapshot.Payload["cloud_network"].(*model.CloudNetwork)
	if !ok || (snapshot.Kind != "ecs" && snapshot.Kind != "swas") {
		return nil
	}
	var raw string
	err := tx.QueryRowContext(ctx, "SELECT payload_json FROM resource_snapshot WHERE account_id=? AND kind=? AND id=? AND region=?", accountID, snapshot.Kind, snapshot.ID, snapshot.Region).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var previous struct {
		Network *model.CloudNetwork `json:"cloud_network"`
	}
	if err = json.Unmarshal([]byte(raw), &previous); err != nil {
		return err
	}
	old := previous.Network
	if old == nil || old.Source != current.Source {
		return nil
	}
	if !current.TopologyComplete {
		if current.NetworkType != "unknown" && current.NetworkType != old.NetworkType {
			return nil
		}
		current.NetworkType = old.NetworkType
		current.Streams = old.Streams
		for i := range current.Streams {
			for _, r := range []*model.CloudNetworkRate{&current.Streams[i].Ingress, &current.Streams[i].Egress} {
				r.CheckedAt = current.CheckedAt
				r.LastError = model.Error("metric_topology_unknown")
				r.Status = "unknown"
				if r.Value != nil {
					r.Status = "stale"
				}
			}
		}
		return nil
	}
	if old.NetworkType != current.NetworkType {
		return nil
	}
	byIP := map[string]model.CloudNetworkStream{}
	for _, stream := range old.Streams {
		key := ""
		if stream.IP != nil {
			key = *stream.IP
		}
		byIP[key] = stream
	}
	for i := range current.Streams {
		stream := &current.Streams[i]
		key := ""
		if stream.IP != nil {
			key = *stream.IP
		}
		if old, ok := byIP[key]; ok {
			retainNetworkRate(&stream.Ingress, old.Ingress)
			retainNetworkRate(&stream.Egress, old.Egress)
		}
	}
	return nil
}
func retainNetworkRate(current *model.CloudNetworkRate, previous model.CloudNetworkRate) {
	if previous.Value == nil || previous.ObservedAt == nil || previous.MetricName != current.MetricName || previous.Unit != current.Unit {
		return
	}
	regressed := current.ObservedAt != nil && current.ObservedAt.Before(*previous.ObservedAt)
	if current.Status == "unknown" || regressed {
		current.Value = previous.Value
		current.ObservedAt = previous.ObservedAt
		current.Status = "stale"
		if regressed {
			current.LastError = model.Error("metric_sample_regressed")
		}
	}
}
