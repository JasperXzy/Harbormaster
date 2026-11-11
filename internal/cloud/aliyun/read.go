// Package aliyun implements fixed read-only Alibaba Cloud queries. It exposes no
// arbitrary endpoint, arbitrary action, cloud assistant or resource write API.
package aliyun

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
	openapi "github.com/alibabacloud-go/darabonba-openapi/v2/client"
	"github.com/alibabacloud-go/tea/dara"
)

var debugAtStartup = os.Getenv("DEBUG") != ""

type Credentials struct {
	model.Credential
	Site    string
	Regions []string
}
type Error struct{ Code string }

func (e *Error) Error() string  { return e.Code }
func failure(code string) error { return &Error{code} }
func Code(err error) string {
	if err == nil {
		return ""
	}
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	if errors.Is(err, context.Canceled) {
		return "cancelled"
	}
	return "aliyun_unreachable"
}

type definition struct{ product, version, params string }

var reads = map[string]definition{
	"DescribeRegions":                 {"ecs", "2014-05-26", ""},
	"DescribeInstances":               {"ecs", "2014-05-26", "RegionId PageNumber PageSize"},
	"DescribeNetworkInterfaces":       {"ecs", "2014-05-26", "RegionId PageNumber PageSize"},
	"DescribeEipAddresses":            {"vpc", "2016-04-28", "RegionId PageNumber PageSize"},
	"DescribeCommonBandwidthPackages": {"vpc", "2016-04-28", "RegionId PageNumber PageSize"},
	"ListInstances":                   {"swas", "2020-06-01", "RegionId PageNumber PageSize"},
	"ListInstancesTrafficPackages":    {"swas", "2020-06-01", "RegionId InstanceIds"},
	"ListCdtInternetTraffic":          {"cdt", "2021-08-13", "RegionId"},
	"DescribeMetricMetaList":          {"cms", "2019-01-01", "Namespace MetricName PageNumber PageSize"},
	"DescribeMetricList":              {"cms", "2019-01-01", "Namespace MetricName Dimensions StartTime EndTime Period"},
	"QueryAccountBalance":             {"bss", "2017-12-14", ""},
}

type Caller interface {
	Call(context.Context, Credentials, string, string, map[string]string) (map[string]any, error)
}
type Client struct {
	http     *http.Client
	interval time.Duration
	mu       sync.Mutex
	next     time.Time
}

func NewClient(transport http.RoundTripper, interval time.Duration) *Client {
	if transport == nil {
		transport = &http.Transport{Proxy: nil, MaxIdleConns: 20, MaxIdleConnsPerHost: 2, IdleConnTimeout: 90 * time.Second}
	}
	return &Client{http: &http.Client{Transport: transport, Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, interval: interval}
}
func Endpoint(product, region, site string) string {
	switch product {
	case "ecs":
		if region == "cn-hangzhou" {
			return "ecs-cn-hangzhou.aliyuncs.com"
		}
		return "ecs." + region + ".aliyuncs.com"
	case "vpc", "swas":
		return product + "." + region + ".aliyuncs.com"
	case "cms":
		return "metrics." + region + ".aliyuncs.com"
	case "cdt":
		return "cdt.aliyuncs.com"
	case "bss":
		if site == "international" {
			return "business.ap-southeast-1.aliyuncs.com"
		}
		return "business.aliyuncs.com"
	}
	return ""
}
func (c *Client) Call(ctx context.Context, cred Credentials, action, region string, query map[string]string) (map[string]any, error) {
	d, ok := reads[action]
	if !ok {
		return nil, failure("operation_not_allowed")
	}
	if debugAtStartup || os.Getenv("DEBUG") != "" {
		return nil, failure("sdk_debug_must_be_disabled")
	}
	if cred.Site != "china" && cred.Site != "international" {
		return nil, failure("invalid_site")
	}
	if !model.RegionPattern.MatchString(region) || len(region) > 64 {
		return nil, failure("invalid_region")
	}
	allowed := false
	for _, r := range cred.Regions {
		if r == region {
			allowed = true
		}
	}
	if d.product == "cdt" {
		if cred.Site != "china" {
			return nil, failure("cdt_international_endpoint_unverified")
		}
		allowed = region == "cn-hongkong"
	}
	if !allowed {
		return nil, failure("region_not_enabled")
	}
	for k, v := range query {
		if !strings.Contains(" "+d.params+" ", " "+k+" ") || (k == "RegionId" && v != region) {
			return nil, failure("parameter_not_allowed")
		}
	}
	c.mu.Lock()
	at := c.next
	if at.Before(time.Now()) {
		at = time.Now()
	}
	c.next = at.Add(c.interval)
	c.mu.Unlock()
	timer := time.NewTimer(time.Until(at))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timer.C:
	}
	method := "GET"
	if d.product == "cdt" {
		method = "POST"
	}
	endpoint := Endpoint(d.product, region, cred.Site)
	gate := readHTTP{client: c.http, endpoint: endpoint, action: action, version: d.version, method: method}
	sdk, err := openapi.NewClient(&openapi.Config{AccessKeyId: dara.String(cred.AccessKeyID), AccessKeySecret: dara.String(cred.AccessKeySecret), Endpoint: dara.String(endpoint), RegionId: dara.String(region), Protocol: dara.String("https"), UserAgent: dara.String("harbormaster"), HttpClient: gate, RetryOptions: &dara.RetryOptions{Retryable: false, MaxAttempts: 1}})
	if err != nil {
		return nil, failure("sdk_initialization_failed")
	}
	sdk.DisableSDKError = dara.Bool(true)
	if d.product == "cdt" {
		sdk.SignatureAlgorithm = dara.String("v2")
	}
	params := &openapi.Params{Action: dara.String(action), Version: dara.String(d.version), Protocol: dara.String("HTTPS"), Pathname: dara.String("/"), Method: dara.String(method), AuthType: dara.String("AK"), Style: dara.String("RPC"), ReqBodyType: dara.String("formData"), BodyType: dara.String("json")}
	q := map[string]*string{}
	for k, v := range query {
		q[k] = dara.String(v)
	}
	callCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	response, err := sdk.CallApiWithCtx(callCtx, params, &openapi.OpenApiRequest{Query: q}, &dara.RuntimeOptions{Autoretry: dara.Bool(false), MaxAttempts: dara.Int(1)})
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		var coded interface{ GetCode() *string }
		if errors.As(err, &coded) {
			return nil, failure(classify(dara.StringValue(coded.GetCode())))
		}
		return nil, failure("aliyun_unreachable")
	}
	body, ok := response["body"].(map[string]any)
	if !ok {
		return nil, failure("invalid_response")
	}
	if success, exists := body["Success"]; exists && (success == false || success == "false") {
		code, _ := body["Code"].(string)
		return nil, failure(classify(code))
	}
	if d.product == "cdt" {
		if code, ok := body["Code"].(string); ok && code != "" && code != "200" && !strings.EqualFold(code, "ok") && !strings.EqualFold(code, "success") {
			return nil, failure(classify(code))
		}
	}
	if d.product == "cdt" {
		if _, direct := body["TrafficDetails"]; !direct {
			if _, nested := valueAt(body, "Data.TrafficDetails"); !nested {
				return nil, failure("invalid_response")
			}
		}
	}
	return body, nil
}

// Return stable categories, never arbitrary SDK errors or server messages.
func classify(code string) string {
	c := strings.ToLower(code)
	switch {
	case strings.Contains(c, "invalidaccesskey"), strings.Contains(c, "signaturedoesnotmatch"), strings.Contains(c, "signaturemismatch"), strings.Contains(c, "invalidsecuritytoken"):
		return "access_key_invalid"
	case strings.Contains(c, "forbidden"), strings.Contains(c, "nopermission"), strings.Contains(c, "unauthorized"):
		return "permission_denied"
	case strings.Contains(c, "throttl"), strings.Contains(c, "limitexceed"):
		return "aliyun_throttled"
	case strings.Contains(c, "invalidregion"), strings.Contains(c, "regionnotsupport"):
		return "region_unavailable"
	default:
		return "aliyun_unreachable"
	}
}

type readHTTP struct {
	client                            *http.Client
	endpoint, action, version, method string
}
type boundedBody struct {
	io.Reader
	io.Closer
}

func header(h http.Header, name string) string {
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
func (g readHTTP) Call(r *http.Request, _ *http.Transport) (*http.Response, error) {
	if r.URL.Scheme != "https" || r.URL.Host != g.endpoint || r.URL.User != nil || r.URL.Path != "/" || r.Method != g.method || header(r.Header, "x-acs-action") != g.action || header(r.Header, "x-acs-version") != g.version {
		return nil, errors.New("request_not_allowed")
	}
	if a := r.URL.Query().Get("Action"); a != "" && a != g.action {
		return nil, errors.New("request_not_allowed")
	}
	res, err := g.client.Do(r)
	if err != nil {
		return nil, errors.New("request_failed")
	}
	res.Body = boundedBody{io.LimitReader(res.Body, 8<<20), res.Body}
	return res, nil
}
