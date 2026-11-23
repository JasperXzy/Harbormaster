package devmode

import (
	"net/http"
	"strings"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
)

func (h *handler) finance(w http.ResponseWriter, path string, now time.Time) bool {
	parts := strings.Split(strings.TrimPrefix(path, "/accounts/"), "/")
	if !strings.HasPrefix(path, "/accounts/") || len(parts) != 2 || parts[1] != "finance" {
		return false
	}
	id := parts[0]
	if id != "demo-account-1" && id != "demo-account-2" {
		failure(w, 404, "not_found")
		return true
	}
	cycle := now.In(h.loc).Format("2006-01")
	currency := "CNY"
	if id == "demo-account-2" {
		currency = "USD"
	}
	respond(w, 200, model.AccountFinance{AccountID: id, Enabled: true, Cycle: cycle, UpdatedAt: &now,
		Balance: model.FinanceSource[model.BalanceData]{Status: "ok", FetchedAt: &now, LastAttemptAt: &now, Data: &model.BalanceData{Currency: currency, Available: "123.45", Cash: "123.45"}},
		Bill:    model.FinanceSource[model.BillData]{Status: "ok", FetchedAt: &now, LastAttemptAt: &now, Data: &model.BillData{Cycle: cycle, Items: []model.BillItem{{Type: "PayAsYouGoBill", ProductCode: "ecs", ProductName: "ECS (demo)", Currency: currency, Pretax: "12.3456", Payment: "12", Outstanding: "0.3456"}}}},
	})
	return true
}

func (h *handler) instanceFinance(w http.ResponseWriter, path string, instances []object, now time.Time) bool {
	parts := strings.Split(strings.TrimPrefix(path, "/instances/"), "/")
	if !strings.HasPrefix(path, "/instances/") || len(parts) != 2 || parts[1] != "finance" {
		return false
	}
	for _, instance := range instances {
		if instance["id"] != parts[0] {
			continue
		}
		cycle := now.In(h.loc).Format("2006-01")
		kind := instance["kind"].(string)
		product := kind
		if kind == "swas" {
			product = "ace_eweb"
		}
		item := model.InstanceBillItem{CloudID: instance["cloud_instance_id"].(string), Type: "PayAsYouGoBill", ProductCode: product, PipCode: kind, ProductName: "Demo", Currency: "CNY", Pretax: "12.3456", AfterDiscount: "15.1"}
		if kind == "swas" {
			item.Type = "SubscriptionOrder"
		}
		source := model.FinanceSource[model.InstanceBillData]{Status: "ok", FetchedAt: &now, LastAttemptAt: &now, Data: &model.InstanceBillData{Cycle: cycle, Items: []model.InstanceBillItem{item}}}
		respond(w, 200, model.InstanceFinance{InstanceID: parts[0], AccountID: instance["account_id"].(string), Enabled: true, Cycle: cycle, UpdatedAt: &now, Bill: source})
		return true
	}
	failure(w, 404, "not_found")
	return true
}
