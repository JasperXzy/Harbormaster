package store

import (
	"context"
	"database/sql"
	"strconv"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
)

func checkBalanceObservation(ctx context.Context, tx *sql.Tx, a model.Account, observation model.FinanceObservation) error {
	if observation.Kind != "balance" || observation.Error != nil || observation.Balance == nil {
		return nil
	}
	rule, err := readAlertRule(tx.QueryRowContext(ctx, "SELECT key,enabled,params_json,urgent FROM alert_rule WHERE key='balance_low'"))
	if err != nil || !rule.Enabled {
		return err
	}
	for _, threshold := range *rule.Params.BalanceThresholds {
		if threshold.AccountID != a.ID || threshold.Currency != observation.Balance.Currency {
			continue
		}
		balance, ok := model.DecimalAmount(observation.Balance.Available)
		limit, valid := model.DecimalAmount(threshold.Amount)
		if !ok || !valid || observation.FetchedAt.IsZero() {
			return nil
		}
		o := alertObservation{rule: rule.Key, target: a.ID, account: a.ID, outcome: "healthy"}
		if balance.Cmp(limit) < 0 {
			o.outcome = "firing"
			o.detail = "available_amount=" + observation.Balance.Available + "; currency=" + threshold.Currency + "; threshold=" + threshold.Amount
		}
		return applyAlert(ctx, tx, o, observation.FetchedAt)
	}
	return nil
}

// Close only removed or re-denominated targets. Amount-only edits await fresh
// evidence; editing another account must not duplicate an existing incident.
func updateBalanceTargets(ctx context.Context, tx *sql.Tx, old, current model.AlertRule, now time.Time) error {
	if current.Key != "balance_low" {
		return nil
	}
	currencies := map[string]string{}
	fields := map[string]string{}
	for index, threshold := range *current.Params.BalanceThresholds {
		var exists int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM account WHERE id=?", threshold.AccountID).Scan(&exists); err != nil {
			return err
		}
		if exists == 0 {
			fields["params.balance_thresholds."+strconv.Itoa(index)+".account_id"] = "not_found"
		}
		currencies[threshold.AccountID] = threshold.Currency
	}
	if len(fields) > 0 {
		return &AlertValidationError{Fields: fields}
	}
	for _, threshold := range *old.Params.BalanceThresholds {
		if currencies[threshold.AccountID] == threshold.Currency {
			continue
		}
		if _, err := tx.ExecContext(ctx, "UPDATE alert_state SET active=0,changed_at=? WHERE rule_key='balance_low' AND target_id=?", timestamp(now), threshold.AccountID); err != nil {
			return err
		}
		if err := cancelPendingNotifications(ctx, tx, "rule_key='balance_low' AND account_id=?", []any{threshold.AccountID}, "alert_rule_changed", now); err != nil {
			return err
		}
	}
	return nil
}
