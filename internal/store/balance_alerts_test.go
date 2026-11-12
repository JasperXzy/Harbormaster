package store

import (
	"context"
	"testing"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
)

func setBalanceRule(t *testing.T, s *Store, now time.Time, thresholds ...model.BalanceThreshold) {
	t.Helper()
	values := append([]model.BalanceThreshold{}, thresholds...)
	if _, err := s.UpdateAlertRule(context.Background(), "balance_low", model.AlertRuleUpdate{Params: &model.AlertParams{BalanceThresholds: &values}}, now); err != nil {
		t.Fatal(err)
	}
}
func balanceScan(t *testing.T, s *Store, now time.Time, currency, amount, code string) {
	t.Helper()
	o := model.FinanceObservation{Kind: "balance", FetchedAt: now, Balance: &model.BalanceData{Currency: currency, Available: amount, Cash: "0"}}
	if code != "" {
		o.Error = model.Error(code)
	}
	j, a := startIPv6Scan(t, s, now)
	if err := s.SaveScan(context.Background(), j, a, model.ScanResult{Finance: []model.FinanceObservation{o}}, now); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishJob(context.Background(), j, nil, "", now); err != nil {
		t.Fatal(err)
	}
}
func balanceHistory(t *testing.T, s *Store) []model.Notification {
	t.Helper()
	result := []model.Notification{}
	for _, n := range alertHistory(t, s) {
		if n.RuleKey != nil && *n.RuleKey == "balance_low" {
			result = append(result, n)
		}
	}
	return result
}

func TestLowBalanceExactThresholdUnknownAndRecovery(t *testing.T) {
	s := agentStore(t)
	setAlertAccount(t, s, "ok", 0)
	now := time.Now().UTC()
	// Empty configuration never guesses a financial threshold.
	balanceScan(t, s, now, "CNY", "-1000", "")
	if len(balanceHistory(t, s)) != 0 {
		t.Fatal("unconfigured threshold fired")
	}
	setBalanceRule(t, s, now, model.BalanceThreshold{AccountID: "a", Currency: "CNY", Amount: "0.1"})
	balanceScan(t, s, now, "CNY", "0.1", "")
	if len(balanceHistory(t, s)) != 0 {
		t.Fatal("equality or cash-only balance triggered")
	}
	for range 2 {
		balanceScan(t, s, now, "CNY", "0.099999999999", "")
		evaluate(t, s, now)
	}
	history := balanceHistory(t, s)
	if len(history) != 1 || history[0].Kind != "alert" || history[0].Severity != "warning" {
		t.Fatal(history)
	}
	balanceScan(t, s, now, "CNY", "100", "permission_denied")
	evaluate(t, s, now)
	balanceScan(t, s, now, "USD", "100", "")
	evaluate(t, s, now)
	balanceScan(t, s, now, "CNY", "NaN", "")
	evaluate(t, s, now)
	if len(balanceHistory(t, s)) != 1 {
		t.Fatal("failure, currency mismatch or malformed source created recovery")
	}
	balanceScan(t, s, now, "CNY", "0.100000000000", "")
	history = balanceHistory(t, s)
	if len(history) != 2 || history[0].Kind != "recovery" {
		t.Fatal(history)
	}
	balanceScan(t, s, now, "CNY", "-1", "")
	if len(balanceHistory(t, s)) != 3 {
		t.Fatal("new incident not recorded")
	}
	setBalanceRule(t, s, now, model.BalanceThreshold{AccountID: "a", Currency: "CNY", Amount: "-10"})
	evaluate(t, s, now)
	if len(balanceHistory(t, s)) != 3 {
		t.Fatal("threshold edit evaluated cached data")
	}
	balanceScan(t, s, now, "CNY", "-1", "")
	if history := balanceHistory(t, s); len(history) != 4 || history[0].Kind != "recovery" {
		t.Fatal(history)
	}
}

func TestBalanceCurrencyAndTargetChangesDoNotReplayOtherIncidents(t *testing.T) {
	s := agentStore(t)
	setAlertAccount(t, s, "ok", 0)
	now := time.Now().UTC()
	ctx := context.Background()
	channelFixture(t, s, 0, now)
	if _, err := s.DB.Exec("INSERT INTO account VALUES('b','second','international','now')"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`INSERT INTO account_settings(account_id,config_json,enabled) VALUES('b','{"id":"b","name":"second","site":"international","enabled":true}',1)`); err != nil {
		t.Fatal(err)
	}
	a := model.BalanceThreshold{AccountID: "a", Currency: "CNY", Amount: "5"}
	b := model.BalanceThreshold{AccountID: "b", Currency: "USD", Amount: "10"}
	setBalanceRule(t, s, now, a)
	balanceScan(t, s, now, "CNY", "1", "")
	setBalanceRule(t, s, now, a, b)
	balanceScan(t, s, now, "CNY", "1", "")
	if history := balanceHistory(t, s); len(history) != 1 || history[0].DeliveryStatus != "pending" {
		t.Fatal("unrelated target edit reset incident", history)
	}
	a.Currency = "USD"
	setBalanceRule(t, s, now, a, b)
	if history := balanceHistory(t, s); len(history) != 1 || history[0].DeliveryStatus == "pending" {
		t.Fatal("old-currency pending notice survived", history)
	}
	balanceScan(t, s, now, "CNY", "1", "")
	if len(balanceHistory(t, s)) != 1 {
		t.Fatal("wrong currency applied")
	}
	balanceScan(t, s, now, "USD", "1", "")
	if len(balanceHistory(t, s)) != 2 {
		t.Fatal("new currency did not start a new incident")
	}
	setBalanceRule(t, s, now, b)
	evaluate(t, s, now)
	if history := balanceHistory(t, s); len(history) != 2 || history[0].DeliveryStatus == "pending" {
		t.Fatal("removed target not cancelled", history)
	}
	setBalanceRule(t, s, now, a, b)
	balanceScan(t, s, now, "USD", "1", "")
	if len(balanceHistory(t, s)) != 3 {
		t.Fatal("readded target did not evaluate")
	}
	if _, err := s.UpdateAccount(ctx, "a", func(a *model.Account) ([]byte, error) { a.Enabled = false; return nil, nil }, now, time.Minute); err != nil {
		t.Fatal(err)
	}
	for _, n := range balanceHistory(t, s) {
		if n.DeliveryStatus == "pending" {
			t.Fatal("disabled account has pending balance alert")
		}
	}
}

func TestBalanceNotificationAndFinanceAreAtomic(t *testing.T) {
	s := agentStore(t)
	setAlertAccount(t, s, "ok", 0)
	now := time.Now().UTC()
	ctx := context.Background()
	setBalanceRule(t, s, now, model.BalanceThreshold{AccountID: "a", Currency: "CNY", Amount: "5"})
	balanceScan(t, s, now, "CNY", "100", "")
	if _, err := s.DB.Exec("CREATE TRIGGER fail_balance_notice BEFORE INSERT ON notification BEGIN SELECT RAISE(ABORT,'test notification failure'); END"); err != nil {
		t.Fatal(err)
	}
	j, a := startIPv6Scan(t, s, now)
	r := model.ScanResult{Failure: "permission_denied", Finance: []model.FinanceObservation{
		{Kind: "balance", FetchedAt: now, Balance: &model.BalanceData{Currency: "CNY", Available: "1", Cash: "1"}},
		{Kind: "bill", Cycle: "2026-11", FetchedAt: now, Error: model.Error("permission_denied")},
	}}
	if err := s.SaveScan(ctx, j, a, r, now); err == nil {
		t.Fatal("failed notice accepted")
	}
	finance, err := s.AccountFinance(ctx, "a", "2026-11")
	if err != nil || finance.Balance.Data == nil || finance.Balance.Data.Available != "100" || len(balanceHistory(t, s)) != 0 {
		t.Fatal("partial finance/notification commit", finance, err)
	}
	if _, err := s.DB.Exec("DROP TRIGGER fail_balance_notice"); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveScan(ctx, j, a, r, now); err != nil {
		t.Fatal(err)
	}
	if len(balanceHistory(t, s)) != 1 {
		t.Fatal("retry did not produce one incident")
	}
}
