package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/JasperXzy/harbormaster/internal/model"
)

func retainCPU(ctx context.Context, tx *sql.Tx, accountID string, snapshot model.Snapshot) error {
	cpu, ok := snapshot.Payload["cpu"].(*model.InstanceCPU)
	if (snapshot.Kind != "ecs" && snapshot.Kind != "swas") || !ok {
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
		CPU *model.InstanceCPU `json:"cpu"`
	}
	if err = json.Unmarshal([]byte(raw), &previous); err != nil {
		return err
	}
	if previous.CPU == nil {
		return nil
	}
	retainMetric(&cpu.Usage, previous.CPU.Usage)
	if cpu.CreditBalance != nil && previous.CPU.CreditBalance != nil {
		retainMetric(cpu.CreditBalance, *previous.CPU.CreditBalance)
	}
	if cpu.CreditMax24h == nil && previous.CPU.CreditMax24h != nil {
		v := *previous.CPU.CreditMax24h
		cpu.CreditMax24h = &v
	}
	return nil
}
func retainMetric(current *model.CPUMetric, previous model.CPUMetric) {
	if previous.Value == nil || previous.ObservedAt == nil || previous.MetricName != current.MetricName || previous.Unit != current.Unit {
		return
	}
	regressed := current.ObservedAt != nil && current.ObservedAt.Before(*previous.ObservedAt)
	if current.Status == "unknown" || regressed {
		current.Value, current.ObservedAt = previous.Value, previous.ObservedAt
		current.Status = "stale"
		if regressed {
			current.LastError = model.Error("metric_sample_regressed")
		}
	}
}
