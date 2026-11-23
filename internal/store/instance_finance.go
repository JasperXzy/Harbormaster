package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/JasperXzy/harbormaster/internal/model"
)

func (s *Store) InstanceFinance(ctx context.Context, id, cycle string) (model.InstanceFinance, error) {
	var result model.InstanceFinance
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	var accountID, cloudID, kind string
	err = tx.QueryRowContext(ctx, "SELECT account_id,cloud_id,kind FROM instance WHERE id=?", id).Scan(&accountID, &cloudID, &kind)
	if errors.Is(err, sql.ErrNoRows) {
		return result, ErrNotFound
	}
	if err != nil {
		return result, err
	}
	a, err := loadAccount(ctx, tx, accountID)
	if err != nil {
		return result, err
	}
	source, err := readFinance[model.InstanceBillData](ctx, tx, a, "instance_bill", cycle)
	if err != nil {
		return result, err
	}
	if source.Data != nil {
		rows := []model.InstanceBillItem{}
		for _, row := range source.Data.Items {
			// Exact IDs only. Shared EIP/CDT/bandwidth rows are never distributed.
			if row.CloudID == cloudID && row.PipCode == kind && (kind == "ecs" || kind == "swas") {
				rows = append(rows, row)
			}
		}
		source.Data.Items = rows
	}
	result = model.InstanceFinance{InstanceID: id, AccountID: accountID, Enabled: a.Enabled, Cycle: cycle, UpdatedAt: source.LastAttemptAt, Bill: source}
	return result, tx.Commit()
}
