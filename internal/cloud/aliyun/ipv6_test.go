package aliyun

import (
	"context"
	"fmt"
	"net/http"
	"reflect"
	"testing"
	"time"
)

func ipv6ENI(id, instance string, addresses ...string) map[string]any {
	items := []any{}
	for _, address := range addresses {
		items = append(items, map[string]any{"Ipv6Address": address})
	}
	return map[string]any{"NetworkInterfaceId": id, "InstanceId": instance, "Ipv6Sets": map[string]any{"Ipv6Set": items}}
}
func ipv6Page(next string, rows ...any) map[string]any {
	if rows == nil {
		rows = []any{}
	}
	return map[string]any{"NetworkInterfaceSets": map[string]any{"NetworkInterfaceSet": rows}, "NextToken": next}
}

func TestIPv6InstanceExtensionsAndFallbackPagination(t *testing.T) {
	primary := ipv6ENI("eni-primary", "i-one", "2001:db8::2", "2001:0DB8:0:0:0:0:0:2")
	secondary := ipv6ENI("eni-secondary", "i-one", "2001:db8::1")
	instance := map[string]any{"InstanceId": "i-one", "NetworkInterfaces": map[string]any{"NetworkInterface": []any{primary, secondary}}}
	calls, fences := 0, 0
	c := Inventory{Caller: callFunc(func(ctx context.Context, _ Credentials, action, region string, q map[string]string) (map[string]any, error) {
		calls++
		deadline, bounded := ctx.Deadline()
		if action != "DescribeNetworkInterfaces" || region != "cn-shanghai" || q["InstanceId"] != "i-one" || q["MaxResults"] != "100" || q["Type"] != "" || !bounded || time.Until(deadline) > 3*time.Second {
			t.Fatal("fallback query lost scope, types or deadline", q)
		}
		if calls == 1 {
			return ipv6Page("next", primary), nil
		}
		if q["NextToken"] != "next" {
			t.Fatal("pagination token missing")
		}
		return ipv6Page("", secondary), nil
	})}
	before := func() error { fences++; return nil }
	addresses, source, err := c.instanceIPv6(context.Background(), fakeCredentials(), "cn-shanghai", instance, before)
	if err != nil || source != "ecs_instances" || calls != 0 || !reflect.DeepEqual(addresses, []string{"2001:db8::1", "2001:db8::2"}) {
		t.Fatal(addresses, source, err, calls)
	}
	delete(instance, "NetworkInterfaces")
	addresses, source, err = c.instanceIPv6(context.Background(), fakeCredentials(), "cn-shanghai", instance, before)
	if err != nil || source != "ecs_network_interfaces" || calls != 2 || fences != 2 || !reflect.DeepEqual(addresses, []string{"2001:db8::1", "2001:db8::2"}) {
		t.Fatal(addresses, source, err, calls, fences)
	}
}

func TestIPv6RejectsIncompleteOrForeignResults(t *testing.T) {
	for _, mode := range []string{"foreign", "duplicate", "repeated_token", "empty_next_page", "missing_field", "malformed_ip", "mapped_ipv4", "zone", "permission", "second_page_failure", "invalid_token"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			c := Inventory{Caller: callFunc(func(context.Context, Credentials, string, string, map[string]string) (map[string]any, error) {
				calls++
				row := ipv6ENI(fmt.Sprint("eni-", calls), "i-one", "2001:db8::1")
				page := ipv6Page("", row)
				switch mode {
				case "foreign":
					row["InstanceId"] = "i-other"
				case "duplicate":
					page = ipv6Page("", row, row)
				case "repeated_token":
					page["NextToken"] = "same"
				case "empty_next_page":
					page = ipv6Page("same")
				case "missing_field":
					delete(row, "Ipv6Sets")
				case "malformed_ip", "mapped_ipv4", "zone":
					address := map[string]string{"malformed_ip": "invalid", "mapped_ipv4": "::ffff:192.0.2.1", "zone": "fe80::1%eth0"}[mode]
					page = ipv6Page("", ipv6ENI("eni", "i-one", address))
				case "permission":
					return nil, failure("permission_denied")
				case "second_page_failure":
					if calls == 2 {
						return nil, failure("permission_denied")
					}
					page["NextToken"] = "next"
				case "invalid_token":
					page["NextToken"] = 123
				}
				return page, nil
			})}
			addresses, _, err := c.instanceIPv6(context.Background(), fakeCredentials(), "cn-shanghai", map[string]any{"InstanceId": "i-one"}, func() error { return nil })
			if err == nil || addresses != nil || calls > 2 {
				t.Fatal("incomplete result accepted", addresses, err, calls)
			}
		})
	}
}

func TestIPv6EmptyExtensionsNeedConfirmedFallback(t *testing.T) {
	for _, rows := range [][]any{{}, {ipv6ENI("eni", "i-one")}} {
		calls := 0
		c := Inventory{Caller: callFunc(func(context.Context, Credentials, string, string, map[string]string) (map[string]any, error) {
			calls++
			return ipv6Page("", rows...), nil
		})}
		instance := map[string]any{"InstanceId": "i-one", "NetworkInterfaces": map[string]any{"NetworkInterface": []any{ipv6ENI("eni", "i-one")}}}
		addresses, source, err := c.instanceIPv6(context.Background(), fakeCredentials(), "cn-shanghai", instance, func() error { return nil })
		if err != nil || addresses == nil || len(addresses) != 0 || source != "ecs_network_interfaces" || calls != 1 {
			t.Fatal(addresses, source, err, calls)
		}
		_, _, err = c.instanceIPv6(context.Background(), fakeCredentials(), "cn-shanghai", instance, func() error { return failure("account_changed") })
		if Code(err) != "account_changed" || calls != 1 {
			t.Fatal("fence did not prevent fallback", calls, err)
		}
	}
}

func TestIPv6FallbackHTTPDeadline(t *testing.T) {
	calls := 0
	client := NewClient(roundTrip(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Query().Get("InstanceId") != "i-one" || r.URL.Query().Get("MaxResults") != "100" {
			t.Fatal("SDK did not preserve fallback parameters")
		}
		<-r.Context().Done()
		return nil, r.Context().Err()
	}), 0)
	start := time.Now()
	_, _, err := (Inventory{Caller: client}).instanceIPv6(context.Background(), fakeCredentials(), "cn-shanghai", map[string]any{"InstanceId": "i-one"}, func() error { return nil })
	if Code(err) != "ipv6_lookup_timeout" || calls != 1 || time.Since(start) > 5*time.Second {
		t.Fatal("fallback HTTP deadline not enforced", err, calls, time.Since(start))
	}
}

func TestIPv6ScanRequestsExtensionsAndKeepsBillingSeparate(t *testing.T) {
	fallbacks := 0
	c := Inventory{Caller: callFunc(func(ctx context.Context, cred Credentials, action, region string, q map[string]string) (map[string]any, error) {
		if action == "DescribeInstances" && q["AdditionalAttributes.1"] != "NETWORK_PRIMARY_ENI_IP" {
			t.Fatal("missing instance extension request")
		}
		if action == "DescribeNetworkInterfaces" && q["InstanceId"] != "" {
			fallbacks++
			return ipv6Page("", ipv6ENI("eni-"+q["InstanceId"], q["InstanceId"], "2001:db8::1")), nil
		}
		return inventoryFixture(ctx, cred, action, region, q)
	})}
	r := c.Scan(context.Background(), testAccount(), fakeCredentials().Credential, false, func() error { return nil })
	if r.Failure != "" || fallbacks != 2 {
		t.Fatal(r.Failure, fallbacks)
	}
	for _, snapshot := range r.Snapshots {
		if snapshot.Kind == "ecs" {
			view := snapshot.Payload["ipv6"].(map[string]any)
			if view["status"] != "ok" || snapshot.Payload["billing_subject_kind"] != "cbwp_dominant_traffic" || len(snapshot.Payload["public_ips"].([]map[string]any)) != 1 {
				t.Fatal("IPv6 changed billing or IPv4", snapshot.Payload)
			}
		}
		if snapshot.Kind == "swas" && snapshot.Payload["ipv6"] != nil {
			t.Fatal("unverified SWAS source used")
		}
	}
}
