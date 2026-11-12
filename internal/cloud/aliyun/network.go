package aliyun

import (
	"context"
	"encoding/json"
	"io"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
)

type networkTarget struct{ ID, IP string }
type networkBinding struct {
	target networkTarget
	stream *model.CloudNetworkStream
}

func (c Inventory) attachNetworkRates(ctx context.Context, cred Credentials, region string, ecs collection, snapshots []model.Snapshot, before func() error, observe func(string, error)) {
	kinds := map[string]string{}
	for _, r := range ecs.rows {
		kinds[text(r, "InstanceId")] = strings.ToLower(text(r, "InstanceNetworkType"))
	}
	groups := map[string][]networkBinding{}
	for _, s := range snapshots {
		if s.Kind != "ecs" && s.Kind != "swas" {
			continue
		}
		id := text(s.Payload, "cloud_instance_id")
		kind := kinds[id]
		source := "cms"
		if s.Kind == "swas" {
			kind = "swas"
			source = "swas_describe_monitor_data"
		}
		if kind != "classic" && kind != "vpc" && kind != "swas" {
			kind = "unknown"
		}
		n := &model.CloudNetwork{Source: source, NetworkType: kind, TopologyComplete: kind != "unknown" && (kind != "vpc" || s.IPv4Complete), CheckedAt: time.Now().UTC(), Streams: []model.CloudNetworkStream{}}
		s.Payload["cloud_network"] = n
		if !n.TopologyComplete {
			continue
		}
		in, out := model.NetworkMetricNames(kind)
		targets := []networkTarget{{ID: id}}
		if kind == "vpc" {
			targets = nil
			ips, _ := s.Payload["public_ips"].([]map[string]any)
			for _, ip := range ips {
				address := ipAddress(text(ip, "address"))
				if address != "" {
					targets = append(targets, networkTarget{ID: id, IP: address})
				}
			}
			sort.Slice(targets, func(i, j int) bool { return targets[i].IP < targets[j].IP })
		}
		for _, target := range targets {
			var ip *string
			if target.IP != "" {
				v := target.IP
				ip = &v
			}
			n.Streams = append(n.Streams, model.CloudNetworkStream{IP: ip, Ingress: model.EmptyNetworkRate(in, n.CheckedAt), Egress: model.EmptyNetworkRate(out, n.CheckedAt)})
		}
		// Store pointers only after appending all streams, avoiding slice relocation.
		for i, target := range targets {
			groups[kind] = append(groups[kind], networkBinding{target, &n.Streams[i]})
		}
	}
	for _, kind := range []string{"classic", "vpc", "swas"} {
		group := groups[kind]
		sort.Slice(group, func(i, j int) bool {
			if group[i].target.ID == group[j].target.ID {
				return group[i].target.IP < group[j].target.IP
			}
			return group[i].target.ID < group[j].target.ID
		})
		size := 50
		if kind == "swas" {
			size = 1
		}
		in, out := model.NetworkMetricNames(kind)
		for start := 0; start < len(group); start += size {
			batch := group[start:min(start+size, len(group))]
			targets := make([]networkTarget, 0, len(batch))
			for _, b := range batch {
				targets = append(targets, b.target)
			}
			for direction, name := range []string{in, out} {
				readings, err := c.networkMetric(ctx, cred, region, targets, kind, name, before)
				permission := "cms_read"
				if kind == "swas" {
					permission = "swas_read"
				}
				observe(permission, err)
				for _, b := range batch {
					if direction == 0 {
						b.stream.Ingress = readings[b.target]
					} else {
						b.stream.Egress = readings[b.target]
					}
				}
			}
		}
	}
}

func (c Inventory) networkMetric(ctx context.Context, cred Credentials, region string, targets []networkTarget, kind, name string, before func() error) (map[networkTarget]model.CloudNetworkRate, error) {
	now := time.Now().UTC()
	start := now.Add(-15 * time.Minute)
	result := map[networkTarget]model.CloudNetworkRate{}
	for _, target := range targets {
		result[target] = model.EmptyNetworkRate(name, now)
	}
	fail := func(err error) (map[networkTarget]model.CloudNetworkRate, error) {
		for target := range result {
			r := model.EmptyNetworkRate(name, time.Now().UTC())
			r.LastError = model.Error(Code(err))
			result[target] = r
		}
		return result, err
	}
	in, out := model.NetworkMetricNames(kind)
	if in == "" || (name != in && name != out) || len(targets) == 0 || len(targets) > 50 || (kind == "swas" && len(targets) != 1) || len(result) != len(targets) {
		return fail(failure("invalid_metric_request"))
	}
	dims := make([]map[string]string, 0, len(targets))
	for _, t := range targets {
		if !powerIDPattern.MatchString(t.ID) {
			return fail(failure("invalid_metric_request"))
		}
		d := map[string]string{"instanceId": t.ID}
		if kind == "vpc" {
			ip, err := netip.ParseAddr(t.IP)
			if err != nil || !ip.Is4() || ip.String() != t.IP {
				return fail(failure("invalid_metric_request"))
			}
			d["ip"] = t.IP
		} else if t.IP != "" {
			return fail(failure("invalid_metric_request"))
		}
		dims = append(dims, d)
	}
	raw, _ := json.Marshal(dims)
	action := "DescribeMetricList"
	query := map[string]string{"Namespace": "acs_ecs_dashboard", "MetricName": name, "Dimensions": string(raw), "StartTime": strconv.FormatInt(start.UnixMilli(), 10), "EndTime": strconv.FormatInt(now.UnixMilli(), 10), "Period": "60", "Length": "1440"}
	if kind == "swas" {
		action = "DescribeMonitorData"
		query = map[string]string{"RegionId": region, "InstanceId": targets[0].ID, "MetricName": name, "StartTime": start.Format(time.RFC3339), "EndTime": now.Format(time.RFC3339), "Period": "60", "Length": "1440"}
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
			v, ok := integer(period)
			if !ok || v != 60 {
				return fail(failure("invalid_response"))
			}
		}
		encoded, ok := body["Datapoints"].(string)
		if !ok || len(encoded) > 4<<20 {
			return fail(failure("invalid_response"))
		}
		// SWAS can return an empty string for unavailable metrics. It is unknown,
		// never a zero sample; malformed nonempty payloads remain errors.
		if encoded == "" {
			encoded = "[]"
		}
		decoder := json.NewDecoder(strings.NewReader(encoded))
		decoder.UseNumber()
		var points []map[string]any
		if decoder.Decode(&points) != nil || points == nil || decoder.Decode(new(any)) != io.EOF || len(points) > 1440 {
			return fail(failure("invalid_response"))
		}
		for _, point := range points {
			id, valid := point["instanceId"].(string)
			target := networkTarget{ID: id}
			if kind == "vpc" {
				ip, ok := point["ip"].(string)
				if !ok || ipAddress(ip) != ip || ip == "" {
					return fail(failure("invalid_response"))
				}
				target.IP = ip
			}
			reading, found := result[target]
			if !valid || !found {
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
			if !ok || value < 0 {
				return fail(failure("invalid_response"))
			}
			key := id + ":" + target.IP + ":" + strconv.FormatInt(millis, 10)
			if previous, exists := seen[key]; exists && previous != value {
				return fail(failure("invalid_response"))
			}
			seen[key] = value
			if reading.ObservedAt == nil || sampled.After(*reading.ObservedAt) {
				reading.Value = &value
				reading.ObservedAt = &sampled
				reading.Status = "ok"
				reading.LastError = nil
				result[target] = reading
			}
		}
		token := ""
		if v, exists := body["NextToken"]; exists && v != nil {
			var ok bool
			token, ok = v.(string)
			if !ok || len(token) > 4096 {
				return fail(failure("invalid_response"))
			}
		}
		if token == "" {
			for target, r := range result {
				r.CheckedAt = time.Now().UTC()
				r.Age(r.CheckedAt)
				result[target] = r
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
