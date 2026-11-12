package aliyun

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
)

func (c Inventory) instanceBills(ctx context.Context, cred Credentials, cycle string, before func() error) (*model.InstanceBillData, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	result := &model.InstanceBillData{Cycle: cycle, Items: []model.InstanceBillItem{}}
	q := map[string]string{"BillingCycle": cycle, "Granularity": "MONTHLY", "IsBillingItem": "false", "MaxResults": "300"}
	seen := map[string]bool{}
	total := -1
	for page := 0; page < 50; page++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err := before(); err != nil {
			return nil, err
		}
		body, err := c.Caller.Call(ctx, cred, "DescribeInstanceBill", cred.Regions[0], q)
		if err != nil {
			return nil, err
		}
		d := child(body, "Data")
		rows, ok := objects(d["Items"])
		if body["Success"] != true || text(d, "BillingCycle") != cycle || !ok || len(rows) > 300 {
			return nil, failure("instance_bill_response_unrecognized")
		}
		// A changing total or a terminal partial response cannot replace a complete cache.
		if raw, exists := d["TotalCount"]; exists {
			var n string
			switch v := raw.(type) {
			case json.Number:
				n = string(v)
			case string:
				n = v
			default:
				return nil, failure("instance_bill_response_unrecognized")
			}
			count, e := strconv.Atoi(n)
			if e != nil || count < 0 || count > 15000 || total >= 0 && count != total {
				return nil, failure("instance_bill_incomplete")
			}
			total = count
		}
		for _, row := range rows {
			amount, aOK := money(row["PretaxAmount"])
			discount, dOK := money(row["AfterDiscountAmount"])
			item := model.InstanceBillItem{CloudID: text(row, "InstanceID"), Type: text(row, "Item"), ProductCode: text(row, "ProductCode"), PipCode: text(row, "PipCode"), ProductName: text(row, "ProductName"), Currency: text(row, "Currency"), Pretax: amount, AfterDiscount: discount}
			if !aOK || !dOK || !currencyPattern.MatchString(item.Currency) || item.CloudID == "" || len(item.CloudID) > 1024 || item.Type == "" || len(item.Type) > 128 || len(item.ProductCode) > 128 || len(item.PipCode) > 128 || len(item.ProductName) > 1024 {
				return nil, failure("instance_bill_response_unrecognized")
			}
			result.Items = append(result.Items, item)
		}
		token := ""
		if raw, exists := d["NextToken"]; exists && raw != nil {
			var valid bool
			token, valid = raw.(string)
			if !valid || len(token) > 8192 {
				return nil, failure("instance_bill_response_unrecognized")
			}
		}
		if token == "" {
			if total >= 0 && total != len(result.Items) {
				return nil, failure("instance_bill_incomplete")
			}
			return result, nil
		}
		if seen[token] {
			return nil, failure("instance_bill_incomplete")
		}
		seen[token] = true
		q["NextToken"] = token
	}
	return nil, failure("instance_bill_incomplete")
}
