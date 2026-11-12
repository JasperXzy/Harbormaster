package m0

import (
	"context"
	"testing"
)

func TestECSMetricSchemaPlanReadsFullCatalogWithoutGuessingMetricNames(t *testing.T) {
	p := exampleProfile()
	ops, err := plan(p, queryOptions{Check: "ecs-metric-schema"})
	if err != nil || len(ops) != 1 {
		t.Fatal(ops, err)
	}
	o := ops[0]
	if o.Action != "DescribeMetricMetaList" || o.Query["Namespace"] != "acs_ecs_dashboard" || o.Query["MetricName"] != "" || o.Query["PageSize"] != "30" || o.PageKey != "PageNumber" || o.ItemsPath != "Resources.Resource" {
		t.Fatal(o)
	}
	calls := 0
	result := execute(context.Background(), p, ops, func(ctx context.Context, p profile, o operation) (map[string]any, string) {
		calls++
		if o.Action != "DescribeMetricMetaList" {
			t.Fatal("not metadata read")
		}
		n := 30
		if o.Query["PageNumber"] == "2" {
			n = 1
		}
		items := make([]any, n)
		for i := range items {
			items[i] = map[string]any{"MetricName": "test-catalog-entry"}
		}
		return map[string]any{"TotalCount": "31", "Resources": map[string]any{"Resource": items}}, ""
	}, 0)
	if calls != 2 || len(result) != 1 || !result[0].Complete || result[0].Status != "passed" || len(result[0].Pages) != 2 {
		t.Fatal(calls, result)
	}
}

func TestECSMetricDataUsesVerifiedNamesAndOnlyInstanceDimension(t *testing.T) {
	p := exampleProfile()
	p.Regions = p.Regions[:1]
	ops, err := plan(p, queryOptions{Check: "metrics", MetricSource: "ecs", ResourceID: "i-example"})
	if err != nil || len(ops) != 2 {
		t.Fatal(ops, err)
	}
	for i, name := range []string{"CPUUtilization", "TotalCredit"} {
		o := ops[i]
		if o.Action != "DescribeMetricList" || o.Query["Namespace"] != "acs_ecs_dashboard" || o.Query["MetricName"] != name || o.Query["Dimensions"] != `[{"instanceId":"i-example"}]` || o.Query["Period"] != "60" {
			t.Fatal(o)
		}
	}
}

func TestSWASCPUPlanIsFixedReadOnlyInstanceQuery(t *testing.T) {
	p := exampleProfile()
	p.Regions = p.Regions[:1]
	ops, err := plan(p, queryOptions{Check: "swas-cpu", ResourceID: "swas-test"})
	if err != nil || len(ops) != 1 {
		t.Fatal(ops, err)
	}
	o := ops[0]
	if o.Action != "DescribeMonitorData" || o.Product != "swas" || o.Query["InstanceId"] != "swas-test" || o.Query["MetricName"] != "CPU_UTILIZATION" || o.Query["Period"] != "60" || o.Query["RegionId"] != p.Regions[0] {
		t.Fatal(o)
	}
}
