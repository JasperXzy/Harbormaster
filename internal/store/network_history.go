package store

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"net/netip"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
)

// Called inside SaveScan's job/account-fenced transaction, after instance upsert.
// Only the complete query's original samples are written, never retained latest
// values or artificial zeros for missing points.
func saveNetworkHistory(ctx context.Context, tx *sql.Tx, snapshot model.Snapshot) error {
	network, ok := snapshot.Payload["cloud_network"].(*model.CloudNetwork)
	if !ok || !network.TopologyComplete {
		return nil
	}
	in, out := model.NetworkMetricNames(network.NetworkType)
	if in == "" {
		return nil
	}
	expectedSource := "cms"
	if network.NetworkType == "swas" {
		expectedSource = "swas_describe_monitor_data"
	}
	if network.Source != expectedSource {
		return errors.New("invalid_network_history_source")
	}
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO cloud_network_sample(instance_id,source,network_type,ip,direction,metric_name,period_seconds,sampled_at,value,checked_at)
 VALUES(?,?,?,?,?,?,?,?,?,?) ON CONFLICT(instance_id,source,network_type,ip,direction,sampled_at)
 DO UPDATE SET value=excluded.value,checked_at=excluded.checked_at WHERE excluded.checked_at>=cloud_network_sample.checked_at`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, stream := range network.Streams {
		ip := ""
		if stream.IP != nil {
			ip = *stream.IP
		}
		if network.NetworkType == "vpc" {
			address, err := netip.ParseAddr(ip)
			if err != nil || !address.Is4() || address.String() != ip {
				return errors.New("invalid_network_history_ip")
			}
		} else if ip != "" {
			return errors.New("invalid_network_history_ip")
		}
		for direction, rate := range map[string]model.CloudNetworkRate{"ingress": stream.Ingress, "egress": stream.Egress} {
			expected := in
			if direction == "egress" {
				expected = out
			}
			if rate.MetricName != expected || rate.Unit != "bit/s" || rate.PeriodSeconds != 60 || len(rate.Samples) > 5760 {
				return errors.New("invalid_network_history_metric")
			}
			for _, point := range rate.Samples {
				if point.SampledAt.IsZero() || point.SampledAt.After(rate.CheckedAt) || math.IsNaN(point.Value) || math.IsInf(point.Value, 0) || point.Value < 0 {
					return errors.New("invalid_network_history_sample")
				}
				if _, err := stmt.ExecContext(ctx, snapshot.ID, network.Source, network.NetworkType, ip, direction, rate.MetricName, rate.PeriodSeconds, timestamp(point.SampledAt), point.Value, timestamp(rate.CheckedAt)); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
func (s *Store) CloudNetworkHistory(ctx context.Context, f model.CloudNetworkHistoryFilter, limit int) ([]model.CloudNetworkSample, error) {
	items := []model.CloudNetworkSample{}
	if !f.Valid(time.Now().UTC()) || limit < 1 || limit > 1001 {
		return nil, errors.New("invalid_history_query")
	}
	var exists int
	if err := s.DB.QueryRowContext(ctx, "SELECT count(*) FROM instance WHERE id=?", f.InstanceID).Scan(&exists); err != nil {
		return nil, err
	}
	if exists != 1 {
		return nil, ErrNotFound
	}
	query := `SELECT sequence,source,network_type,ip,direction,metric_name,period_seconds,value,sampled_at,checked_at
 FROM cloud_network_sample WHERE instance_id=? AND sampled_at>=? AND sampled_at<?`
	args := []any{f.InstanceID, timestamp(f.From), timestamp(f.To)}
	if f.Direction != "" {
		query += " AND direction=?"
		args = append(args, f.Direction)
	}
	if f.IP != "" {
		query += " AND ip=?"
		args = append(args, f.IP)
	}
	if !f.BeforeTime.IsZero() {
		query += " AND (sampled_at<? OR (sampled_at=? AND sequence<?))"
		args = append(args, timestamp(f.BeforeTime), timestamp(f.BeforeTime), f.BeforeSequence)
	}
	query += " ORDER BY sampled_at DESC,sequence DESC LIMIT ?"
	args = append(args, limit)
	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var v model.CloudNetworkSample
		var ip, sampled, checked string
		if err = rows.Scan(&v.Sequence, &v.Source, &v.NetworkType, &ip, &v.Direction, &v.MetricName, &v.PeriodSeconds, &v.Value, &sampled, &checked); err != nil {
			return nil, err
		}
		if ip != "" {
			v.IP = &ip
		}
		v.Unit = "bit/s"
		if v.SampledAt, err = time.Parse(time.RFC3339Nano, sampled); err != nil {
			return nil, err
		}
		if v.CheckedAt, err = time.Parse(time.RFC3339Nano, checked); err != nil {
			return nil, err
		}
		items = append(items, v)
	}
	return items, rows.Err()
}
