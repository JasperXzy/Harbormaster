package m0

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	openapi "github.com/alibabacloud-go/darabonba-openapi/v2/client"
	"github.com/alibabacloud-go/tea/dara"
)

// SDK debug hooks capture DEBUG during package initialization and can print
// signed requests. Refuse queries in that process, rather than trying to unset it.
var sdkDebugAtStartup = os.Getenv("DEBUG") != ""

type operation struct {
	Product   string            `json:"product"`
	Action    string            `json:"action"`
	Version   string            `json:"version"`
	Region    string            `json:"region"`
	Endpoint  string            `json:"endpoint"`
	Query     map[string]string `json:"query"`
	PageKey   string            `json:"page_key,omitempty"`
	ItemsPath string            `json:"items_path,omitempty"`
}
type definition struct{ product, version, params string }

// Exact allowlist, deliberately no arbitrary RPC command or endpoint flags.
var allowed = map[string]definition{
	// ActionTrail documents this read; the request shape comes from the PRD reference project.
	"ListCdtInternetTraffic":          {"cdt", "2021-08-13", "RegionId"},
	"DescribeRegions":                 {"ecs", "2014-05-26", ""},
	"DescribeInstances":               {"ecs", "2014-05-26", "RegionId PageNumber PageSize"},
	"DescribeNetworkInterfaces":       {"ecs", "2014-05-26", "RegionId PageNumber PageSize"},
	"DescribeEipAddresses":            {"vpc", "2016-04-28", "RegionId PageNumber PageSize"},
	"DescribeCommonBandwidthPackages": {"vpc", "2016-04-28", "RegionId PageNumber PageSize"},
	"ListInstances":                   {"swas", "2020-06-01", "RegionId PageNumber PageSize"},
	"ListInstancesTrafficPackages":    {"swas", "2020-06-01", "RegionId InstanceIds"},
	"QueryAccountBalance":             {"bss", "2017-12-14", ""},
	"QueryBillOverview":               {"bss", "2017-12-14", "BillingCycle"},
	"QueryInstanceBill":               {"bss", "2017-12-14", "BillingCycle Granularity IsBillingItem PageNum PageSize"},
	"DescribeMetricList":              {"cms", "2019-01-01", "Namespace MetricName Dimensions StartTime EndTime Period"},
	"DescribeMetricMetaList":          {"cms", "2019-01-01", "Namespace MetricName PageNumber PageSize"},
}

func endpoint(product, region, site string) string {
	switch product {
	case "ecs":
		if region == "cn-hangzhou" {
			return "ecs-cn-hangzhou.aliyuncs.com"
		}
		return "ecs." + region + ".aliyuncs.com"
	case "vpc":
		return "vpc." + region + ".aliyuncs.com"
	case "swas":
		return "swas." + region + ".aliyuncs.com"
	case "cdt":
		return "cdt.aliyuncs.com"
	case "cms":
		return "metrics." + region + ".aliyuncs.com"
	case "bss":
		if site == "international" {
			return "business.ap-southeast-1.aliyuncs.com"
		}
		return "business.aliyuncs.com"
	}
	return ""
}
func newOperation(p profile, action, region string, q map[string]string, page, items string) operation {
	d := allowed[action]
	return operation{d.product, action, d.version, region, endpoint(d.product, region, p.Site), q, page, items}
}
func (o operation) validate(p profile) error {
	d, ok := allowed[o.Action]
	if !ok || d.product != o.Product || d.version != o.Version {
		return errors.New("operation_not_allowed")
	}
	regionOK := false
	for _, r := range p.Regions {
		if r == o.Region {
			regionOK = true
		}
	}
	// CDT service region is not a resource-discovery region.
	if o.Product == "cdt" && o.Region == "cn-hongkong" {
		regionOK = true
	}
	if !regionOK || o.Endpoint != endpoint(d.product, o.Region, p.Site) {
		return errors.New("endpoint_not_allowed")
	}
	for k := range o.Query {
		if !strings.Contains(" "+d.params+" ", " "+k+" ") {
			return errors.New("parameter_not_allowed")
		}
	}
	if r, ok := o.Query["RegionId"]; ok && r != o.Region {
		return errors.New("region_mismatch")
	}
	return nil
}

type queryOptions struct{ Check, Month, MetricSource, ResourceID string }

func plan(p profile, options queryOptions) ([]operation, error) {
	if err := p.validate(); err != nil {
		return nil, err
	}
	ops := []operation{}
	switch options.Check {
	case "metric-schema":
		for _, metric := range [][2]string{{"acs_vpc_eip", "net.rx"}, {"acs_vpc_eip", "net.tx"}, {"acs_bandwidth_package", "net_rx.rate"}, {"acs_bandwidth_package", "net_tx.rate"}} {
			ops = append(ops, newOperation(p, "DescribeMetricMetaList", p.Regions[0], map[string]string{"Namespace": metric[0], "MetricName": metric[1], "PageNumber": "1", "PageSize": "100"}, "", ""))
		}
	case "cdt":
		if p.Site != "china" {
			return nil, errors.New("cdt_international_endpoint_unverified")
		}
		ops = append(ops, newOperation(p, "ListCdtInternetTraffic", "cn-hongkong", map[string]string{"RegionId": "cn-hongkong"}, "", ""))
	case "inventory":
		ops = append(ops, newOperation(p, "DescribeRegions", p.Regions[0], map[string]string{}, "", ""))
		for _, region := range p.Regions {
			for _, item := range [][2]string{{"DescribeInstances", "Instances.Instance"}, {"DescribeNetworkInterfaces", "NetworkInterfaceSets.NetworkInterfaceSet"}, {"DescribeEipAddresses", "EipAddresses.EipAddress"}, {"DescribeCommonBandwidthPackages", "CommonBandwidthPackages.CommonBandwidthPackage"}, {"ListInstances", "Instances"}} {
				size := "100"
				if item[0] == "DescribeCommonBandwidthPackages" {
					size = "50"
				}
				ops = append(ops, newOperation(p, item[0], region, map[string]string{"RegionId": region, "PageNumber": "1", "PageSize": size}, "PageNumber", item[1]))
			}
		}
	case "billing":
		if _, err := time.Parse("2006-01", options.Month); err != nil {
			return nil, errors.New("billing_month_required_yyyy_mm")
		}
		ops = append(ops, newOperation(p, "QueryAccountBalance", p.Regions[0], map[string]string{}, "", ""), newOperation(p, "QueryBillOverview", p.Regions[0], map[string]string{"BillingCycle": options.Month}, "", ""), newOperation(p, "QueryInstanceBill", p.Regions[0], map[string]string{"BillingCycle": options.Month, "Granularity": "MONTHLY", "IsBillingItem": "true", "PageNum": "1", "PageSize": "100"}, "PageNum", "Data.Items.Item"))
	case "metrics":
		if !regexp.MustCompile(`^[a-zA-Z0-9-]{1,128}$`).MatchString(options.ResourceID) {
			return nil, errors.New("metric_resource_id_required")
		}
		var ns string
		var names []string
		switch options.MetricSource {
		case "eip":
			ns = "acs_vpc_eip"
			names = []string{"net.rx", "net.tx"}
		case "bandwidth":
			ns = "acs_bandwidth_package"
			names = []string{"net_rx.rate", "net_tx.rate"}
		default:
			return nil, errors.New("metric_source_must_be_eip_or_bandwidth")
		}
		// One resource belongs to one region; do not silently try it in all regions.
		if len(p.Regions) != 1 {
			return nil, errors.New("metrics_require_one_selected_region")
		}
		dim, _ := json.Marshal([]map[string]string{{"instanceId": options.ResourceID}})
		end := time.Now().UTC()
		for _, name := range names {
			ops = append(ops, newOperation(p, "DescribeMetricList", p.Regions[0], map[string]string{"Namespace": ns, "MetricName": name, "Dimensions": string(dim), "StartTime": end.Add(-time.Hour).Format(time.RFC3339), "EndTime": end.Format(time.RFC3339), "Period": "60"}, "", ""))
		}
	default:
		return nil, errors.New("check_must_be_inventory_billing_metrics_metric_schema_or_cdt")
	}
	return ops, nil
}

type sdkCaller struct{ client *http.Client }
type callFunc func(context.Context, profile, operation) (map[string]any, string)

type gatedHTTP struct {
	client *http.Client
	op     operation
}
type boundedBody struct {
	io.Reader
	io.Closer
}

// The SDK assigns lowercase map keys directly, rather than Header.Set. Header.Get
// alone would miss these keys. Multiple case variants are rejected.
func sdkHeader(h http.Header, name string) string {
	value := ""
	found := false
	for k, vs := range h {
		if strings.EqualFold(k, name) {
			if found || len(vs) != 1 {
				return ""
			}
			found = true
			value = vs[0]
		}
	}
	return value
}

func (g gatedHTTP) Call(r *http.Request, _ *http.Transport) (*http.Response, error) {
	if r.URL.Scheme != "https" || r.URL.Host != g.op.Endpoint || r.URL.User != nil || r.URL.Path != "/" || r.Method != methodFor(g.op) || sdkHeader(r.Header, "x-acs-action") != g.op.Action || sdkHeader(r.Header, "x-acs-version") != g.op.Version {
		return nil, errors.New("request_not_allowed")
	}
	if a := r.URL.Query().Get("Action"); a != "" && a != g.op.Action {
		return nil, errors.New("request_not_allowed")
	}
	res, err := g.client.Do(r)
	if err != nil {
		return nil, errors.New("request_failed")
	}
	res.Body = boundedBody{io.LimitReader(res.Body, 8<<20), res.Body}
	return res, nil
}

func (s sdkCaller) call(ctx context.Context, p profile, o operation) (map[string]any, string) {
	if sdkDebugAtStartup || os.Getenv("DEBUG") != "" {
		return nil, "sdk_debug_must_be_disabled"
	}
	if o.validate(p) != nil {
		return nil, "operation_not_allowed"
	}
	c, err := openapi.NewClient(&openapi.Config{AccessKeyId: dara.String(p.AccessKeyID), AccessKeySecret: dara.String(p.AccessKeySecret), SecurityToken: dara.String(p.SecurityToken), Endpoint: dara.String(o.Endpoint), RegionId: dara.String(o.Region), Protocol: dara.String("https"), UserAgent: dara.String("harbormaster-m0"), EndpointSuffix: dara.String("aliyuncs.com"), HttpClient: gatedHTTP{s.client, o}, RetryOptions: &dara.RetryOptions{Retryable: false, MaxAttempts: 1}})
	if err != nil {
		return nil, "sdk_initialization_failed"
	}
	c.DisableSDKError = dara.Bool(true)
	if o.Product == "cdt" {
		c.SignatureAlgorithm = dara.String("v2")
	}
	q := map[string]*string{}
	for k, v := range o.Query {
		q[k] = dara.String(v)
	}
	params := &openapi.Params{Action: dara.String(o.Action), Version: dara.String(o.Version), Protocol: dara.String("HTTPS"), Pathname: dara.String("/"), Method: dara.String(methodFor(o)), AuthType: dara.String("AK"), Style: dara.String("RPC"), ReqBodyType: dara.String("formData"), BodyType: dara.String("json")}
	callCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	result, err := c.CallApiWithCtx(callCtx, params, &openapi.OpenApiRequest{Query: q}, &dara.RuntimeOptions{Autoretry: dara.Bool(false), MaxAttempts: dara.Int(1)})
	if err != nil {
		var coded interface{ GetCode() *string }
		if errors.As(err, &coded) {
			code := dara.StringValue(coded.GetCode())
			if safeCode.MatchString(code) {
				return nil, scrubText(code, p)
			}
		}
		return nil, "request_failed"
	}
	body, ok := result["body"].(map[string]any)
	if !ok {
		return nil, "invalid_response"
	}
	if success, exists := body["Success"]; exists && (success == false || success == "false") {
		code, _ := body["Code"].(string)
		if !safeCode.MatchString(code) {
			code = "api_reported_failure"
		}
		return nil, scrubText(code, p)
	}
	if o.Product == "cdt" {
		if code, ok := body["Code"].(string); ok && code != "" && code != "200" && !strings.EqualFold(code, "ok") && !strings.EqualFold(code, "success") {
			if safeCode.MatchString(code) {
				return nil, scrubText(code, p)
			}
			return nil, "api_reported_failure"
		}
		_, direct := body["TrafficDetails"]
		_, nested := atPath(body, "Data.TrafficDetails")
		if !direct && !nested {
			return nil, "cdt_response_shape_unknown"
		}
	}
	return body, ""
}

var safeCode = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,128}$`)

type queryResult struct {
	Action     string `json:"action"`
	MetricName string `json:"metric_name,omitempty"`
	Region     string `json:"region"`
	Status     string `json:"status"`
	ErrorCode  string `json:"error_code,omitempty"`
	Complete   bool   `json:"complete"`
	FetchedAt  string `json:"fetched_at"`
	Pages      []any  `json:"pages"`
}
type report struct {
	Account           string        `json:"account"`
	Check             string        `json:"check"`
	AccessKeyIDMasked string        `json:"access_key_id_masked"`
	CloudWrites       int           `json:"cloud_writes"`
	Results           []queryResult `json:"results"`
	Notes             []string      `json:"notes"`
}

func atPath(body map[string]any, path string) (any, bool) {
	var v any = body
	for _, key := range strings.Split(path, ".") {
		m, ok := v.(map[string]any)
		if !ok {
			return nil, false
		}
		v, ok = m[key]
		if !ok {
			return nil, false
		}
	}
	return v, true
}

func execute(ctx context.Context, p profile, ops []operation, call callFunc, interval time.Duration) []queryResult {
	results := []queryResult{}
	for index := 0; index < len(ops); index++ {
		o := ops[index]
		r := queryResult{Action: o.Action, MetricName: o.Query["MetricName"], Region: o.Region, Status: "passed", Complete: false, Pages: []any{}}
		seen := 0
		for page := 1; page <= 50; page++ {
			if o.PageKey != "" {
				o.Query[o.PageKey] = strconv.Itoa(page)
			}
			if interval > 0 {
				timer := time.NewTimer(interval)
				select {
				case <-ctx.Done():
					timer.Stop()
					r.ErrorCode = "cancelled"
				case <-timer.C:
				}
			}
			if r.ErrorCode != "" {
				break
			}
			body, code := call(ctx, p, o)
			r.FetchedAt = time.Now().UTC().Format(time.RFC3339)
			if code != "" {
				r.ErrorCode = code
				break
			}
			r.Pages = append(r.Pages, scrub(body, p))
			if o.Action == "ListInstances" {
				if raw, ok := body["Instances"].([]any); ok {
					ids := []string{}
					for _, item := range raw {
						if m, ok := item.(map[string]any); ok {
							if id, ok := m["InstanceId"].(string); ok && id != "" {
								ids = append(ids, id)
							}
						}
					}
					for start := 0; start < len(ids); start += 50 {
						batch, _ := json.Marshal(ids[start:min(start+50, len(ids))])
						ops = append(ops, newOperation(p, "ListInstancesTrafficPackages", o.Region, map[string]string{"RegionId": o.Region, "InstanceIds": string(batch)}, "", ""))
					}
				}
			}
			if o.PageKey == "" {
				if token, ok := body["NextToken"].(string); ok && token != "" {
					r.ErrorCode = "continuation_not_followed"
					break
				}
				r.Complete = true
				break
			}
			items, ok := atPath(body, o.ItemsPath)
			list, okList := items.([]any)
			if !ok || !okList {
				r.ErrorCode = "pagination_shape_unknown"
				break
			}
			seen += len(list)
			total, hasTotal := totalCount(body)
			if hasTotal && seen < total && len(list) == 0 {
				r.ErrorCode = "pagination_incomplete"
				break
			}
			pageSize, e := strconv.Atoi(o.Query["PageSize"])
			if e != nil || pageSize < 1 {
				r.ErrorCode = "pagination_size_unknown"
				break
			}
			if (hasTotal && seen >= total) || (!hasTotal && len(list) < pageSize) {
				r.Complete = true
				break
			}
			if page == 50 {
				r.ErrorCode = "page_limit_reached"
			}
		}
		if r.ErrorCode != "" {
			r.Status = "check_failed"
			if strings.Contains(strings.ToLower(r.ErrorCode), "forbidden") || strings.Contains(strings.ToLower(r.ErrorCode), "nopermission") {
				r.Status = "missing"
			}
		}
		results = append(results, r)
		if ctx.Err() != nil {
			break
		}
	}
	return results
}

func totalCount(body map[string]any) (int, bool) {
	v, ok := body["TotalCount"]
	if !ok {
		v, ok = atPath(body, "Data.TotalCount")
	}
	if !ok {
		return 0, false
	}
	switch n := v.(type) {
	case float64:
		if n >= 0 && n <= 1e9 && float64(int(n)) == n {
			return int(n), true
		}
	case int:
		if n >= 0 {
			return n, true
		}
	case string:
		i, e := strconv.Atoi(n)
		return i, e == nil && i >= 0
	}
	return 0, false
}

func methodFor(o operation) string {
	if o.Product == "cdt" {
		return "POST"
	}
	return "GET"
}
