package aliyun

import (
	"context"
	"errors"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
	openapi "github.com/alibabacloud-go/darabonba-openapi/v2/client"
	"github.com/alibabacloud-go/tea/dara"
)

// PowerRequest deliberately has no arbitrary action, endpoint or parameters.
// ClientToken is the persisted operation ID for SWAS idempotence. ECS's three
// single-instance power APIs do not declare ClientToken support.
type PowerRequest struct {
	Kind, Region, InstanceID, Action, StopMode, ClientToken string
}

// Outcome is not_sent, rejected, unknown, or accepted. Accepted means only that
// the asynchronous API accepted the request, never that the instance reached
// its target state or that StopCharging actually took effect.
type PowerResult struct{ Outcome string }

type PowerClient struct{ client *Client }

func NewPowerClient(transport http.RoundTripper, interval time.Duration) *PowerClient {
	return &PowerClient{client: NewClient(transport, interval)}
}

var powerIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
var powerTokenPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

func powerParameters(req PowerRequest) (definition, string, map[string]*string, error) {
	d := definition{product: req.Kind}
	switch req.Kind {
	case "ecs":
		d.version = "2014-05-26"
	case "swas":
		d.version = "2020-06-01"
	default:
		return d, "", nil, failure("invalid_instance_kind")
	}
	if !powerIDPattern.MatchString(req.InstanceID) {
		return d, "", nil, failure("invalid_instance_id")
	}
	action := map[string]string{"start": "StartInstance", "stop": "StopInstance", "reboot": "RebootInstance"}[req.Action]
	if action == "" {
		return d, "", nil, failure("operation_not_allowed")
	}
	if req.Action != "stop" && req.StopMode != "" {
		return d, "", nil, failure("invalid_stop_mode")
	}
	q := map[string]*string{"InstanceId": dara.String(req.InstanceID)}
	if req.Kind == "swas" {
		if req.StopMode != "" || !powerTokenPattern.MatchString(req.ClientToken) {
			return d, "", nil, failure("invalid_power_parameters")
		}
		q["RegionId"] = dara.String(req.Region)
		q["ClientToken"] = dara.String(req.ClientToken)
	} else {
		if req.ClientToken != "" {
			return d, "", nil, failure("invalid_power_parameters")
		}
		switch req.Action {
		case "start":
			q["InitLocalDisk"] = dara.String("false")
		case "stop", "reboot":
			q["ForceStop"] = dara.String("false")
		}
		if req.Action == "stop" {
			mode := map[string]string{"keep_charging": "KeepCharging", "stop_charging": "StopCharging"}[req.StopMode]
			if mode == "" {
				return d, "", nil, failure("invalid_stop_mode")
			}
			q["StoppedMode"] = dara.String(mode)
		}
	}
	return d, action, q, nil
}

// Execute makes at most one transport attempt. In particular, callers must not
// replay an unknown outcome: a lost response can follow a successful reboot.
// This client is not wired into read-only collection or run-once commands.
func (c *PowerClient) Execute(ctx context.Context, cred Credentials, req PowerRequest) (PowerResult, error) {
	result := PowerResult{Outcome: "not_sent"}
	d, action, query, err := powerParameters(req)
	if err != nil {
		return result, err
	}
	if debugAtStartup || os.Getenv("DEBUG") != "" {
		return result, failure("sdk_debug_must_be_disabled")
	}
	if cred.Site != "china" && cred.Site != "international" {
		return result, failure("invalid_site")
	}
	if len(req.Region) > 64 || !model.RegionPattern.MatchString(req.Region) {
		return result, failure("invalid_region")
	}
	allowed := false
	for _, region := range cred.Regions {
		allowed = allowed || region == req.Region
	}
	if !allowed {
		return result, failure("region_not_enabled")
	}
	if err = c.client.wait(ctx); err != nil {
		return result, failure("cancelled")
	}
	endpoint := Endpoint(d.product, req.Region, cred.Site)
	gate := &powerHTTP{gate: readHTTP{client: c.client.http, endpoint: endpoint, action: action, version: d.version, method: "POST"}}
	sdk, err := openapi.NewClient(&openapi.Config{AccessKeyId: dara.String(cred.AccessKeyID), AccessKeySecret: dara.String(cred.AccessKeySecret), Endpoint: dara.String(endpoint), RegionId: dara.String(req.Region), Protocol: dara.String("https"), UserAgent: dara.String("harbormaster"), HttpClient: gate, RetryOptions: &dara.RetryOptions{Retryable: false, MaxAttempts: 1}})
	if err != nil {
		return result, failure("sdk_initialization_failed")
	}
	sdk.DisableSDKError = dara.Bool(true)
	params := &openapi.Params{Action: dara.String(action), Version: dara.String(d.version), Protocol: dara.String("HTTPS"), Pathname: dara.String("/"), Method: dara.String("POST"), AuthType: dara.String("AK"), Style: dara.String("RPC"), ReqBodyType: dara.String("formData"), BodyType: dara.String("json")}
	callCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	response, err := sdk.CallApiWithCtx(callCtx, params, &openapi.OpenApiRequest{Query: query}, &dara.RuntimeOptions{Autoretry: dara.Bool(false), MaxAttempts: dara.Int(1)})
	if !gate.attempted {
		return result, failure("power_request_not_sent")
	}
	result.Outcome = "unknown"
	if err != nil {
		var coded interface{ GetCode() *string }
		// A structured 4xx rejection (excluding request timeout) is definite.
		// Transport failures, 5xx and malformed responses remain uncertain.
		if gate.status >= 400 && gate.status < 500 && gate.status != 408 && errors.As(err, &coded) && dara.StringValue(coded.GetCode()) != "" {
			result.Outcome = "rejected"
			return result, failure(powerErrorCode(dara.StringValue(coded.GetCode())))
		}
		return result, failure("power_outcome_unknown")
	}
	body, ok := response["body"].(map[string]any)
	requestID, _ := body["RequestId"].(string)
	_, hasError := body["Code"]
	if gate.status < 200 || gate.status >= 300 || !ok || strings.TrimSpace(requestID) == "" || hasError {
		return result, failure("power_outcome_unknown")
	}
	return PowerResult{Outcome: "accepted"}, nil
}

func powerErrorCode(code string) string {
	switch strings.ToLower(code) {
	case "incorrectinstancestatus", "incorrectinstancestatus.initializing", "incorrectinstancestatus.running", "incorrectinstancestatus.stopped":
		return "instance_state_conflict"
	case "invalidinstanceid.notfound", "invalidinstanceid.notexist":
		return "instance_not_found"
	}
	if category := classify(code); category != "aliyun_unreachable" {
		return category
	}
	return "power_request_rejected"
}

type powerHTTP struct {
	gate      readHTTP
	attempted bool
	status    int
}

func (g *powerHTTP) Call(r *http.Request, transport *http.Transport) (*http.Response, error) {
	if g.attempted {
		return nil, errors.New("power_retry_not_allowed")
	}
	if err := r.Context().Err(); err != nil {
		return nil, errors.New("power_request_cancelled")
	}
	g.attempted = true
	response, err := g.gate.Call(r, transport)
	if response != nil {
		g.status = response.StatusCode
	}
	return response, err
}
