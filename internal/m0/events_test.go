package m0

import (
	"strconv"
	"testing"
)

func TestEventPlansExplicitlyIncludeUnfinishedAndSeparateSpotSource(t *testing.T) {
	p := exampleProfile()
	ops, err := plan(p, queryOptions{Check: "system-events"})
	if err != nil || len(ops) != 2*len(p.Regions) {
		t.Fatal(ops, err)
	}
	for n, o := range ops {
		if n%2 == 0 {
			if o.Action != "DescribeInstanceHistoryEvents" || o.Query["ResourceType"] != "instance" {
				t.Fatal(o)
			}
			for i, state := range []string{"Scheduled", "Executing", "Inquiring", "Executed", "Canceled", "Avoided", "Failed"} {
				if o.Query["InstanceEventCycleStatus."+strconv.Itoa(i+1)] != state {
					t.Fatal("unfinished events missing")
				}
			}
		} else if o.Action != "DescribeSystemEventAttribute" || o.Query["Product"] != "ECS" || o.Query["Name"] != "Instance:PreemptibleInstanceInterruption" {
			t.Fatal(o)
		}
	}
	schema, err := plan(p, queryOptions{Check: "event-schema"})
	if err != nil || len(schema) != 1 || schema[0].Action != "DescribeSystemEventMetaList" || len(schema[0].Query) != 0 {
		t.Fatal(schema, err)
	}
}
