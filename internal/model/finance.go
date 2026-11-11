package model

import "time"

type BalanceData struct {
	Currency  string  `json:"currency"`
	Available string  `json:"available_amount"`
	Cash      string  `json:"available_cash_amount"`
	Credit    *string `json:"credit_amount"`
}
type BillItem struct {
	Type        string `json:"bill_type"`
	ProductCode string `json:"product_code"`
	ProductName string `json:"product_name"`
	Currency    string `json:"currency"`
	Pretax      string `json:"pretax_amount"`
	Payment     string `json:"payment_amount"`
	Outstanding string `json:"outstanding_amount"`
}
type BillData struct {
	Cycle string     `json:"billing_cycle"`
	Items []BillItem `json:"items"`
}
type FinanceObservation struct {
	Kind, Cycle string
	FetchedAt   time.Time
	Balance     *BalanceData
	Bill        *BillData
	Error       *APIError
}
type FinanceSource[T any] struct {
	Status        string     `json:"status"`
	FetchedAt     *time.Time `json:"fetched_at"`
	LastAttemptAt *time.Time `json:"last_attempt_at"`
	Error         *APIError  `json:"last_error"`
	Data          *T         `json:"data"`
}
type AccountFinance struct {
	AccountID string                     `json:"account_id"`
	Enabled   bool                       `json:"enabled"`
	Cycle     string                     `json:"billing_cycle"`
	UpdatedAt *time.Time                 `json:"updated_at"`
	Balance   FinanceSource[BalanceData] `json:"balance"`
	Bill      FinanceSource[BillData]    `json:"bill"`
}
