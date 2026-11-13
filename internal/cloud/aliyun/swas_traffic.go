package aliyun

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
)

func swasTrafficInteger(v any) (int64, bool) {
	var n int64
	var err error
	switch value := v.(type) {
	case int:
		n = int64(value)
	case int64:
		n = value
	case json.Number:
		n, err = value.Int64()
	case string:
		n, err = strconv.ParseInt(value, 10, 64)
	case float64:
		if value < -model.MaxSWASTrafficBytes || value > model.MaxSWASTrafficBytes || value != float64(int64(value)) {
			return 0, false
		}
		n = int64(value)
	default:
		return 0, false
	}
	return n, err == nil && n >= -model.MaxSWASTrafficBytes && n <= model.MaxSWASTrafficBytes
}

func parseSWASTraffic(body map[string]any, ids []string) (map[string]*model.SWASTrafficUsage, error) {
	rows, ok := objects(body["InstanceTrafficPackageUsages"])
	if !ok || len(rows) > len(ids) {
		return nil, failure("swas_traffic_response_unrecognized")
	}
	allowed := map[string]bool{}
	for _, id := range ids {
		allowed[id] = true
	}
	result := map[string]*model.SWASTrafficUsage{}
	for _, row := range rows {
		id := text(row, "InstanceId")
		if !allowed[id] || result[id] != nil {
			return nil, failure("swas_traffic_response_unrecognized")
		}
		var usage model.SWASTrafficUsage
		for _, field := range []struct {
			name string
			out  *int64
		}{{"TrafficPackageTotal", &usage.Total}, {"TrafficUsed", &usage.Used}, {"TrafficPackageRemaining", &usage.Remaining}, {"TrafficOverflow", &usage.Overflow}} {
			value, valid := swasTrafficInteger(row[field.name])
			if !valid {
				return nil, failure("swas_traffic_response_unrecognized")
			}
			*field.out = value
		}
		if !usage.Valid() {
			return nil, failure("swas_traffic_response_unrecognized")
		}
		result[id] = &usage
	}
	return result, nil
}

// Traffic package reads reuse the existing bounded discovery requests. A bad
// batch cannot turn missing/duplicate/malformed rows into a no-package result.
func (c Inventory) swasTraffic(ctx context.Context, a model.Account, cred Credentials, region string, swas collection, before func() error, observe func(string, error)) ([]model.SWASTrafficObservation, map[string]bool, bool) {
	observations := []model.SWASTrafficObservation{}
	present := map[string]bool{}
	if swas.err != nil {
		return observations, present, false
	}
	complete := true
	for start := 0; start < len(swas.rows); start += 50 {
		batch := swas.rows[start:min(start+50, len(swas.rows))]
		ids := make([]string, 0, len(batch))
		for _, row := range batch {
			ids = append(ids, text(row, "InstanceId"))
		}
		encoded, _ := json.Marshal(ids)
		var body map[string]any
		err := ctx.Err()
		if err == nil {
			err = before()
		}
		if err == nil {
			body, err = c.Caller.Call(ctx, cred, "ListInstancesTrafficPackages", region, map[string]string{"RegionId": region, "InstanceIds": string(encoded)})
		}
		var usages map[string]*model.SWASTrafficUsage
		if err == nil {
			usages, err = parseSWASTraffic(body, ids)
		}
		queriedAt := time.Now().UTC()
		observe("swas_read", err)
		if err != nil {
			complete = false
		}
		for _, row := range batch {
			cloudID := text(row, "InstanceId")
			o := model.SWASTrafficObservation{InstanceID: resourceID(a, "swas", region, cloudID), PlanID: text(row, "PlanId"), QueriedAt: queriedAt}
			if err != nil {
				o.Error = model.Error(Code(err))
			} else {
				o.Usage = usages[cloudID]
				yes := o.Usage != nil
				o.EntryPresent = &yes
				present[cloudID] = yes
			}
			observations = append(observations, o)
		}
	}
	return observations, present, complete
}
