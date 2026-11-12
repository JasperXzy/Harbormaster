package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
)

func (s *Store) BusinessLocation(ctx context.Context, fallback string) (*time.Location, error) {
	if fallback == "" {
		fallback = "Asia/Shanghai"
	}
	zone := fallback
	err := s.DB.QueryRowContext(ctx, "SELECT value FROM app_meta WHERE key='business_timezone'").Scan(&zone)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	return time.LoadLocation(zone)
}

// Called only inside SaveScan's transaction, after account/job revision fencing.
func saveFinance(ctx context.Context, tx *sql.Tx, a model.Account, o model.FinanceObservation) error {
	var data any
	switch o.Kind {
	case "balance":
		if o.Cycle != "" {
			return errors.New("invalid_finance_source")
		}
		if o.Balance != nil {
			data = o.Balance
		}
	case "bill":
		if _, err := time.Parse("2006-01", o.Cycle); err != nil {
			return errors.New("invalid_finance_source")
		}
		if o.Bill != nil && o.Bill.Cycle == o.Cycle {
			data = o.Bill
		}
	default:
		return errors.New("invalid_finance_source")
	}
	var payload, fetched, code any
	if o.Error != nil {
		code = o.Error.Code
	} else {
		if data == nil {
			return errors.New("invalid_finance_source")
		}
		raw, err := json.Marshal(data)
		if err != nil {
			return err
		}
		payload, fetched = string(raw), timestamp(o.FetchedAt)
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO account_finance(account_id,credential_version,source,billing_cycle,fetched_at,last_attempt_at,error_code,payload_json) VALUES(?,?,?,?,?,?,?,?)
 ON CONFLICT(account_id,credential_version,source,billing_cycle) DO UPDATE SET
 last_attempt_at=excluded.last_attempt_at,error_code=excluded.error_code,
 fetched_at=coalesce(excluded.fetched_at,account_finance.fetched_at),
 payload_json=coalesce(excluded.payload_json,account_finance.payload_json)`, a.ID, a.CredentialVersion, o.Kind, o.Cycle, fetched, timestamp(o.FetchedAt), code, payload)
	if err != nil {
		return err
	}
	return checkBalanceObservation(ctx, tx, a, o)
}

func readFinance[T any](ctx context.Context, q queryRower, a model.Account, source, cycle string) (model.FinanceSource[T], error) {
	v := model.FinanceSource[T]{Status: "never_synced"}
	var fetched, attempt, code, raw sql.NullString
	err := q.QueryRowContext(ctx, "SELECT fetched_at,last_attempt_at,error_code,payload_json FROM account_finance WHERE account_id=? AND credential_version=? AND source=? AND billing_cycle=?", a.ID, a.CredentialVersion, source, cycle).Scan(&fetched, &attempt, &code, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return v, nil
	}
	if err != nil {
		return v, err
	}
	v.Status = "ok"
	v.FetchedAt, v.LastAttemptAt = parseAgentTime(fetched), parseAgentTime(attempt)
	if code.Valid {
		v.Status, v.Error = "failing", model.Error(code.String)
	}
	if raw.Valid {
		v.Data = new(T)
		if err := json.Unmarshal([]byte(raw.String), v.Data); err != nil {
			return v, err
		}
	}
	return v, nil
}

func (s *Store) AccountFinance(ctx context.Context, id, cycle string) (model.AccountFinance, error) {
	var result model.AccountFinance
	// One snapshot prevents a credential rotation between reading the account
	// version and reading the cached payload from exposing its predecessor.
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	a, err := loadAccount(ctx, tx, id)
	if err != nil {
		return result, err
	}
	result.AccountID, result.Enabled, result.Cycle = id, a.Enabled, cycle
	result.Balance, err = readFinance[model.BalanceData](ctx, tx, a, "balance", "")
	if err != nil {
		return result, err
	}
	result.Bill, err = readFinance[model.BillData](ctx, tx, a, "bill", cycle)
	if err != nil {
		return result, err
	}
	for _, at := range []*time.Time{result.Balance.LastAttemptAt, result.Bill.LastAttemptAt} {
		if at != nil && (result.UpdatedAt == nil || at.After(*result.UpdatedAt)) {
			result.UpdatedAt = at
		}
	}
	return result, tx.Commit()
}
