package aliyun

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

func ecsEvent(id, status string) map[string]any {
	return map[string]any{"EventId": id, "InstanceId": "i-test", "EventType": map[string]any{"Name": "SystemMaintenance.Reboot", "Code": 1}, "EventCycleStatus": map[string]any{"Name": status, "Code": 0}, "EventPublishTime": "2026-11-01T00:00:00Z", "NotBefore": "2026-12-01T00:00:00Z"}
}
func ecsEvents(rows ...any) map[string]any {
	if rows == nil {
		rows = []any{}
	}
	return map[string]any{"InstanceSystemEventSet": map[string]any{"InstanceSystemEventType": rows}}
}
func cmsEvent(id, region string) map[string]any {
	return map[string]any{"Id": id, "Name": "Instance:PreemptibleInstanceInterruption", "Product": "ECS", "Status": "Normal", "RegionId": region, "ResourceId": "acs:ecs:" + region + ":123456:instance/i-test", "Time": time.Now().Add(-time.Minute).UnixMilli()}
}
func cmsEvents(rows ...any) map[string]any {
	if rows == nil {
		rows = []any{}
	}
	return map[string]any{"Success": "true", "Code": "200", "SystemEvents": map[string]any{"SystemEvent": rows}}
}

func TestSystemEventsIncludePendingAndKeepSeparateSpotEvidence(t *testing.T) {
	calls := map[string]int{}
	c := Inventory{Caller: callFunc(func(ctx context.Context, cred Credentials, action, region string, q map[string]string) (map[string]any, error) {
		calls[action]++
		if action == "DescribeInstanceHistoryEvents" {
			if q["InstanceEventCycleStatus.1"] != "Scheduled" || q["InstanceEventCycleStatus.2"] != "Executing" || q["InstanceEventCycleStatus.3"] != "Inquiring" || q["ResourceType"] != "instance" || q["MaxResults"] != "100" {
				t.Fatal("unfinished events excluded", q)
			}
			if calls[action] == 1 {
				b := ecsEvents(ecsEvent("e-maint", "Scheduled"))
				b["NextToken"] = "next"
				b["TotalCount"] = 0
				return b, nil
			}
			if q["NextToken"] != "next" {
				t.Fatal("token lost")
			}
			e := ecsEvent("e-spot", "Executed")
			e["EventType"] = map[string]any{"Name": "Instance:PreemptibleInstanceInterruption", "Code": 257}
			delete(e, "NotBefore")
			e["EventFinishTime"] = e["EventPublishTime"]
			return ecsEvents(e), nil
		}
		if action != "DescribeSystemEventAttribute" || q["Product"] != "ECS" || q["Name"] != "Instance:PreemptibleInstanceInterruption" {
			t.Fatal(action, q)
		}
		return cmsEvents(cmsEvent("cms-spot", region), cmsEvent("cms-other", "cn-hongkong")), nil
	})}
	result := c.SystemEvents(context.Background(), testAccount(), fakeCredentials().Credential, func() error { return nil })
	if len(result) != 2 || result[0].Error != nil || result[1].Error != nil || len(result[0].Events) != 2 || len(result[1].Events) != 1 {
		t.Fatal(result)
	}
	maintenance, spot := result[0].Events[0], result[0].Events[1]
	if maintenance.Category != "maintenance" || maintenance.NotBefore == nil || spot.Category != "spot_interruption" || spot.ProviderStatus != "Executed" || spot.ProviderStatusCode == nil || *spot.ProviderStatusCode != 0 || spot.NotBefore != nil {
		t.Fatal(maintenance, spot)
	}
	cms := result[1].Events[0]
	if cms.CloudInstanceID != "i-test" || cms.ProviderStatus != "Normal" || cms.NotBefore != nil || cms.FinishedAt != nil || cms.PublishedAt == nil {
		t.Fatal("CMS signal fabricated lifecycle or deadline", cms)
	}
}

func TestSystemEventsDiscardPartialSourcesAndValidateShape(t *testing.T) {
	for _, scenario := range []string{"failed-page", "duplicate", "repeat-token", "invalid-time", "wrong-resource", "missing-id"} {
		t.Run(scenario, func(t *testing.T) {
			calls := 0
			c := Inventory{Caller: callFunc(func(ctx context.Context, cred Credentials, action, region string, q map[string]string) (map[string]any, error) {
				calls++
				if scenario == "failed-page" && calls == 2 {
					return nil, failure("permission_denied")
				}
				id := "e-test"
				if scenario == "repeat-token" {
					id = fmt.Sprintf("e-%d", calls)
				}
				e := ecsEvent(id, "Scheduled")
				switch scenario {
				case "invalid-time":
					e["NotBefore"] = "not-a-date"
				case "wrong-resource":
					e["ResourceType"] = "ddh"
				case "missing-id":
					delete(e, "EventId")
				}
				b := ecsEvents(e)
				if scenario == "failed-page" || scenario == "duplicate" || scenario == "repeat-token" {
					b["NextToken"] = "next"
				}
				return b, nil
			})}
			rows, err := c.systemEventsSource(context.Background(), fakeCredentials(), "cn-shanghai", "ecs_history", func() error { return nil })
			if err == nil || rows != nil {
				t.Fatal("partial or malformed source accepted", rows, err)
			}
		})
	}
}

func TestCMSInterruptionRequiresOwnRegionIdentityAndWindow(t *testing.T) {
	start, end := time.Now().Add(-time.Hour), time.Now()
	valid := cmsEvent("event-test", "cn-shanghai")
	if e, ok, err := parseCMSInterruption(valid, "cn-shanghai", start, end); err != nil || !ok || e.CloudInstanceID != "i-test" {
		t.Fatal(e, ok, err)
	}
	for _, change := range []func(map[string]any){
		func(m map[string]any) { m["Product"] = "RDS" }, func(m map[string]any) { m["Name"] = "other" },
		func(m map[string]any) { m["ResourceId"] = "acs:ecs:cn-hongkong:123456:instance/i-test" },
		func(m map[string]any) { m["ResourceId"] = "acs:ecs:cn-shanghai:123456:disk/d-test" },
		func(m map[string]any) { m["Time"] = end.Add(time.Minute).UnixMilli() },
		func(m map[string]any) { m["Time"] = start.Add(-time.Minute).UnixMilli() },
	} {
		m := cmsEvent("event-test", "cn-shanghai")
		change(m)
		if _, _, err := parseCMSInterruption(m, "cn-shanghai", start, end); err == nil {
			t.Fatal("unsafe CMS event accepted")
		}
	}
	other := cmsEvent("event-other", "cn-hongkong")
	if _, include, err := parseCMSInterruption(other, "cn-shanghai", start, end); err != nil || include {
		t.Fatal(include, err)
	}
	valid["ResourceId"] = "i-test"
	if _, include, err := parseCMSInterruption(valid, "cn-shanghai", start, end); err != nil || !include {
		t.Fatal(include, err)
	}
}

func TestCMSPagesAndQueryFailureRemainIndependentOfECS(t *testing.T) {
	calls := 0
	c := Inventory{Caller: callFunc(func(ctx context.Context, cred Credentials, action, region string, q map[string]string) (map[string]any, error) {
		if action == "DescribeInstanceHistoryEvents" {
			return nil, failure("permission_denied")
		}
		calls++
		if calls == 2 {
			if q["PageNumber"] != "2" {
				t.Fatal("page not incremented")
			}
			return cmsEvents(), nil
		}
		rows := []any{}
		for i := 0; i < 100; i++ {
			rows = append(rows, cmsEvent(fmt.Sprintf("spot-%d", i), region))
		}
		return cmsEvents(rows...), nil
	})}
	result := c.SystemEvents(context.Background(), testAccount(), fakeCredentials().Credential, func() error { return nil })
	if len(result) != 2 || result[0].Events != nil || result[0].Error == nil || result[0].Error.Code != "permission_denied" || result[1].Error != nil || len(result[1].Events) != 100 || calls != 2 {
		t.Fatal(result, calls)
	}
	serialized := fmt.Sprintf("%v", result[1])
	if strings.Contains(serialized, "123456") {
		t.Fatal("ARN account leaked into normalized event")
	}
}
