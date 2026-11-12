package aliyun

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestMoneyKeepsDecimalPrecision(t *testing.T) {
	for input, want := range map[string]string{"0": "0", "-0.000": "0", "1,234.5600": "1234.56", "-12.34": "-12.34", "0.100000000001": "0.100000000001", "1.234e-8": "0.00000001234", "9007199254740993.12": "9007199254740993.12"} {
		if got, ok := money(json.Number(input)); !ok || got != want {
			t.Fatal(input, got, ok)
		}
	}
	for _, input := range []any{nil, float64(0.1), "", "NaN", "1/2", "01", "1,23.45", "0.0000000000001", "1e99999", "1e99", " 1.0", "+2"} {
		if got, ok := money(input); ok {
			t.Fatal("invalid or lossy money accepted", input, got)
		}
	}
}

func TestSignedBSSReadsPreserveJSONNumbersAndSelectSite(t *testing.T) {
	for site, endpoint := range map[string]string{"china": "business.aliyuncs.com", "international": "business.ap-southeast-1.aliyuncs.com"} {
		t.Run(site, func(t *testing.T) {
			cred := fakeCredentials()
			cred.Site = site
			c := NewClient(roundTrip(func(r *http.Request) (*http.Response, error) {
				if r.URL.Host != endpoint || r.Method != "GET" || header(r.Header, "x-acs-action") != "QueryBillOverview" || r.URL.Query().Get("BillingCycle") != "2026-11" || header(r.Header, "Authorization") == "" {
					t.Fatal("incorrect signed finance request")
				}
				return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"Success":true,"Data":{"BillingCycle":"2026-11","Items":{"Item":[{"Item":"Refund","Currency":"USD","PretaxAmount":9007199254740993.12,"PaymentAmount":0.100000000001,"OutstandingAmount":-0.000001}]}}}`))}, nil
			}), 0)
			body, err := c.Call(context.Background(), cred, "QueryBillOverview", "cn-shanghai", map[string]string{"BillingCycle": "2026-11"})
			if err != nil {
				t.Fatal(err)
			}
			bill, err := parseBill(body, "2026-11")
			if err != nil || len(bill.Items) != 1 || bill.Items[0].Pretax != "9007199254740993.12" || bill.Items[0].Payment != "0.100000000001" || bill.Items[0].Outstanding != "-0.000001" || bill.Items[0].Type != "Refund" {
				t.Fatal(bill, err)
			}
		})
	}
}

func TestFinanceMissingDataAndWrongCycleAreNotZero(t *testing.T) {
	for _, raw := range []string{`{}`, `{"Success":true,"Data":{"Currency":"CNY","AvailableAmount":"0"}}`, `{"Success":true,"Data":{"Currency":"CNY","AvailableAmount":"NaN","AvailableCashAmount":"0"}}`} {
		var body map[string]any
		_ = json.Unmarshal([]byte(raw), &body)
		if b, err := parseBalance(body); err == nil || b != nil {
			t.Fatal(b, err)
		}
	}
	body := map[string]any{"Success": true, "Data": map[string]any{"Currency": "USD", "AvailableAmount": "1,000.00", "AvailableCashAmount": "-2.30"}}
	balance, err := parseBalance(body)
	if err != nil || balance.Available != "1000" || balance.Cash != "-2.3" || balance.Credit != nil {
		t.Fatal(balance, err)
	}
	body = map[string]any{"Success": true, "Data": map[string]any{"BillingCycle": "2026-10", "Items": map[string]any{"Item": []any{}}}}
	if bill, err := parseBill(body, "2026-11"); err == nil || bill != nil {
		t.Fatal("wrong month accepted")
	}
	if bill, err := parseBill(body, "2026-10"); err != nil || bill.Items == nil || len(bill.Items) != 0 {
		t.Fatal(bill, err)
	}
}

func TestFinanceSourcesFailIndependentlyAndFenceCalls(t *testing.T) {
	calls := 0
	c := Inventory{Caller: callFunc(func(_ context.Context, _ Credentials, action, _ string, _ map[string]string) (map[string]any, error) {
		calls++
		if action == "DescribeInstanceBill" {
			return map[string]any{"Success": true, "Data": map[string]any{"BillingCycle": "2026-11", "Items": []any{}}}, nil
		}
		if action == "QueryAccountBalance" {
			return nil, failure("permission_denied")
		}
		return map[string]any{"Success": true, "Data": map[string]any{"BillingCycle": "2026-11", "Items": map[string]any{"Item": []any{}}}}, nil
	})}
	items := c.Finance(context.Background(), testAccount(), fakeCredentials().Credential, "2026-11", func() error { return nil })
	if calls != 3 || items[0].Error == nil || items[0].Balance != nil || items[1].Error != nil || items[1].Bill == nil || items[2].Error != nil || items[2].InstanceBill == nil {
		t.Fatal(items)
	}
	c.Finance(context.Background(), testAccount(), fakeCredentials().Credential, "2026-11", func() error { return failure("account_changed") })
	if calls != 3 {
		t.Fatal("stale account reached cloud")
	}
}
