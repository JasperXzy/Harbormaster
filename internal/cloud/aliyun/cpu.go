package aliyun

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
)

func (c Inventory) attachCPU(ctx context.Context, cred Credentials, region string, snapshots []model.Snapshot, before func() error, observe func(string, error)) {
	targets := map[string]*model.InstanceCPU{}
	ids := []string{}
	now := time.Now().UTC()
	for _, s := range snapshots {
		if s.Kind == "swas" {
			id, _ := s.Payload["cloud_instance_id"].(string)
			readings, err := c.cpuMetric(ctx, cred, region, []string{id}, "CPU_UTILIZATION", before)
			observe("swas_read", err)
			s.Payload["cpu"] = &model.InstanceCPU{Source: "swas_describe_monitor_data", Usage: readings[id]}
			continue
		}
		if s.Kind != "ecs" {
			continue
		}
		id, _ := s.Payload["cloud_instance_id"].(string)
		cpu := model.NewECSCPU(now)
		s.Payload["cpu"] = cpu
		if id != "" {
			targets[id] = cpu
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	for start := 0; start < len(ids); start += 50 {
		batch := ids[start:min(start+50, len(ids))]
		for _, name := range []string{"CPUUtilization", "TotalCredit"} {
			readings, err := c.cpuMetric(ctx, cred, region, batch, name, before)
			observe("cms_read", err)
			for _, id := range batch {
				reading := readings[id]
				if name == "CPUUtilization" {
					targets[id].Usage = reading
				} else {
					targets[id].CreditBalance = &reading
				}
			}
		}
	}
}

func (c Inventory) cpuMetric(ctx context.Context, cred Credentials, region string, ids []string, name string, before func() error) (map[string]model.CPUMetric, error) {
	now := time.Now().UTC()
	start := now.Add(-15 * time.Minute)
	result := map[string]model.CPUMetric{}
	for _, id := range ids {
		result[id] = model.EmptyCPUMetric(name, now)
	}
	fail := func(err error) (map[string]model.CPUMetric, error) {
		for id := range result {
			r := model.EmptyCPUMetric(name, time.Now().UTC())
			r.LastError = model.Error(Code(err))
			result[id] = r
		}
		return result, err
	}
	if name != "CPUUtilization" && name != "TotalCredit" && name != "CPU_UTILIZATION" || len(ids) == 0 || len(ids) > 50 || (name == "CPU_UTILIZATION" && len(ids) != 1) {
		return fail(failure("invalid_metric_request"))
	}
	for _, id := range ids {
		if !powerIDPattern.MatchString(id) {
			return fail(failure("invalid_metric_request"))
		}
	}
	dimensions := make([]map[string]string, 0, len(ids))
	for _, id := range ids {
		dimensions = append(dimensions, map[string]string{"instanceId": id})
	}
	raw, _ := json.Marshal(dimensions)
	query := map[string]string{"Namespace": "acs_ecs_dashboard", "MetricName": name, "Dimensions": string(raw), "StartTime": strconv.FormatInt(start.UnixMilli(), 10), "EndTime": strconv.FormatInt(now.UnixMilli(), 10), "Period": "60", "Length": "1440"}
	action := "DescribeMetricList"
	if name == "CPU_UTILIZATION" {
		action = "DescribeMonitorData"
		query = map[string]string{"RegionId": region, "InstanceId": ids[0], "MetricName": name, "Period": "60", "Length": "1440", "StartTime": strconv.FormatInt(start.UnixMilli(), 10), "EndTime": strconv.FormatInt(now.UnixMilli(), 10)}
	}
	readCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tokens := map[string]bool{}
	seen := map[string]float64{}
	for page := 0; page < 4; page++ {
		if err := before(); err != nil {
			return fail(err)
		}
		body, err := c.Caller.Call(readCtx, cred, action, region, query)
		if err != nil {
			return fail(err)
		}
		if success, exists := body["Success"]; exists && success != true {
			return fail(failure("invalid_response"))
		}
		if code, exists := body["Code"]; exists && code != "200" && code != float64(200) && code != 200 {
			return fail(failure("invalid_response"))
		}
		if period, exists := body["Period"]; exists {
			n, valid := integer(period)
			if !valid || n != 60 {
				return fail(failure("invalid_response"))
			}
		}
		encoded, ok := body["Datapoints"].(string)
		if !ok || len(encoded) > 4<<20 {
			return fail(failure("invalid_response"))
		}
		decoder := json.NewDecoder(strings.NewReader(encoded))
		decoder.UseNumber()
		var points []map[string]any
		if decoder.Decode(&points) != nil || points == nil || decoder.Decode(new(any)) != io.EOF || len(points) > 1440 {
			return fail(failure("invalid_response"))
		}
		for _, point := range points {
			id, ok := point["instanceId"].(string)
			reading, found := result[id]
			if !ok || !found {
				return fail(failure("invalid_response"))
			}
			millis, ok := integer(point["timestamp"])
			if !ok {
				return fail(failure("invalid_response"))
			}
			sampled := time.UnixMilli(millis).UTC()
			if !sampled.After(start) || sampled.After(now) {
				return fail(failure("invalid_response"))
			}
			value, ok := metricNumber(point["Average"])
			if !ok || value < 0 || name != "TotalCredit" && value > 100 {
				return fail(failure("invalid_response"))
			}
			pointKey := id + ":" + strconv.FormatInt(millis, 10)
			if previous, exists := seen[pointKey]; exists && previous != value {
				return fail(failure("invalid_response"))
			}
			seen[pointKey] = value
			if reading.ObservedAt == nil || sampled.After(*reading.ObservedAt) {
				reading.Value = &value
				reading.ObservedAt = &sampled
				reading.Status = "ok"
				reading.LastError = nil
				result[id] = reading
			}
		}
		token := ""
		if v, exists := body["NextToken"]; exists && v != nil {
			var valid bool
			token, valid = v.(string)
			if !valid || len(token) > 4096 {
				return fail(failure("invalid_response"))
			}
		}
		if token == "" {
			for id, r := range result {
				r.CheckedAt = time.Now().UTC()
				r.Age(r.CheckedAt)
				result[id] = r
			}
			return result, nil
		}
		if tokens[token] || len(points) == 0 {
			return fail(failure("pagination_incomplete"))
		}
		tokens[token] = true
		query["NextToken"] = token
	}
	return fail(failure("page_limit_reached"))
}
func metricNumber(v any) (float64, bool) {
	var f float64
	var err error
	switch x := v.(type) {
	case json.Number:
		f, err = x.Float64()
	case float64:
		f = x
	default:
		return 0, false
	}
	return f, err == nil && !math.IsNaN(f) && !math.IsInf(f, 0)
}
