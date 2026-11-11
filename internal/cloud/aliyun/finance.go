package aliyun

import (
	"context"
	"encoding/json"
	"math/big"
	"regexp"
	"strings"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
)

var currencyPattern = regexp.MustCompile(`^[A-Z]{3}$`)
var decimalPattern = regexp.MustCompile(`^-?(0|[1-9][0-9]*)(\.[0-9]+)?([eE][+-]?[0-9]{1,2})?$`)
var groupedMoneyPattern = regexp.MustCompile(`^-?[1-9][0-9]{0,2}(,[0-9]{3})+(\.[0-9]+)?$`)

// No binary floating point enters this conversion. At most twelve fractional
// digits are accepted; higher precision is rejected rather than rounded away.
func money(value any) (string, bool) {
	var raw string
	switch n := value.(type) {
	case string:
		raw = n
	case json.Number:
		raw = string(n)
	default:
		return "", false
	}
	if len(raw) > 80 {
		return "", false
	}
	if groupedMoneyPattern.MatchString(raw) {
		raw = strings.ReplaceAll(raw, ",", "")
	}
	if !decimalPattern.MatchString(raw) {
		return "", false
	}
	r, ok := new(big.Rat).SetString(raw)
	if !ok {
		return "", false
	}
	scaled := new(big.Rat).Mul(r, big.NewRat(1000000000000, 1))
	if !scaled.IsInt() || scaled.Num().BitLen() > 256 {
		return "", false
	}
	s := strings.TrimRight(strings.TrimRight(r.FloatString(12), "0"), ".")
	if s == "-0" {
		s = "0"
	}
	return s, true
}

func (c Inventory) Finance(ctx context.Context, a model.Account, credential model.Credential, cycle string, before func() error) []model.FinanceObservation {
	cred := Credentials{Credential: credential, Site: a.Site, Regions: a.RegionIDs}
	result := []model.FinanceObservation{}
	for _, kind := range []string{"balance", "bill"} {
		action, q := "QueryAccountBalance", map[string]string{}
		if kind == "bill" {
			action, q = "QueryBillOverview", map[string]string{"BillingCycle": cycle}
		}
		err := before()
		var body map[string]any
		if err == nil {
			body, err = c.Caller.Call(ctx, cred, action, a.RegionIDs[0], q)
		}
		o := model.FinanceObservation{Kind: kind, FetchedAt: time.Now().UTC()}
		if kind == "bill" {
			o.Cycle = cycle
		}
		if err != nil {
			o.Error = model.Error(Code(err))
		} else if kind == "balance" {
			o.Balance, err = parseBalance(body)
		} else {
			o.Bill, err = parseBill(body, cycle)
		}
		if err != nil {
			o.Error = model.Error(Code(err))
		}
		result = append(result, o)
	}
	return result
}
func parseBalance(body map[string]any) (*model.BalanceData, error) {
	d := child(body, "Data")
	a, aOK := money(d["AvailableAmount"])
	cash, cashOK := money(d["AvailableCashAmount"])
	currency := text(d, "Currency")
	if body["Success"] != true || !aOK || !cashOK || !currencyPattern.MatchString(currency) {
		return nil, failure("balance_response_unrecognized")
	}
	b := &model.BalanceData{Currency: currency, Available: a, Cash: cash}
	if value, exists := d["CreditAmount"]; exists && value != nil {
		credit, ok := money(value)
		if !ok {
			return nil, failure("balance_response_unrecognized")
		}
		b.Credit = &credit
	}
	return b, nil
}
func parseBill(body map[string]any, cycle string) (*model.BillData, error) {
	d := child(body, "Data")
	value, exists := valueAt(d, "Items.Item")
	rows, ok := objects(value)
	if body["Success"] != true || text(d, "BillingCycle") != cycle || !exists || !ok || len(rows) > 10000 {
		return nil, failure("bill_response_unrecognized")
	}
	b := &model.BillData{Cycle: cycle, Items: []model.BillItem{}}
	for _, row := range rows {
		pretax, pOK := money(row["PretaxAmount"])
		payment, payOK := money(row["PaymentAmount"])
		outstanding, oOK := money(row["OutstandingAmount"])
		currency, kind := text(row, "Currency"), text(row, "Item")
		if !pOK || !payOK || !oOK || !currencyPattern.MatchString(currency) || kind == "" || len(kind) > 128 {
			return nil, failure("bill_response_unrecognized")
		}
		b.Items = append(b.Items, model.BillItem{Type: kind, ProductCode: text(row, "ProductCode"), ProductName: text(row, "ProductName"), Currency: currency, Pretax: pretax, Payment: payment, Outstanding: outstanding})
	}
	return b, nil
}
