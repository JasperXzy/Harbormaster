package aliyun

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
	openapi "github.com/alibabacloud-go/darabonba-openapi/v2/client"
	"github.com/alibabacloud-go/tea/dara"
)

// ReleaseRequest is a single ECS target. The primitive cannot force release a
// running instance, terminate a subscription, or change associated resources.
// DeleteInstance does not declare ClientToken support, so none is sent.
type ReleaseRequest struct{ Region, InstanceID string }

// ReleaseResult describes request delivery, not confirmed resource deletion.
// Its outcomes are not_sent, rejected, unknown, and accepted.
type ReleaseResult struct{ Outcome string }

type ReleaseClient struct{ client *Client }

func NewReleaseClient(transport http.RoundTripper, interval time.Duration) *ReleaseClient {
	return &ReleaseClient{client: NewClient(transport, interval)}
}

// Execute sends one fixed DeleteInstance request at most once. Callers must
// persist the dispatch boundary and must never replay an unknown outcome.
// This primitive has no production worker, CLI or panel entry point yet.
func (c *ReleaseClient) Execute(ctx context.Context, cred Credentials, req ReleaseRequest) (ReleaseResult, error) {
	result := ReleaseResult{Outcome: "not_sent"}
	if !powerIDPattern.MatchString(req.InstanceID) {
		return result, failure("invalid_instance_id")
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
	if err := c.client.wait(ctx); err != nil {
		return result, failure("cancelled")
	}
	endpoint := Endpoint("ecs", req.Region, cred.Site)
	gate := &writeHTTP{gate: readHTTP{client: c.client.http, endpoint: endpoint, action: "DeleteInstance", version: "2014-05-26", method: "POST"}}
	sdk, err := openapi.NewClient(&openapi.Config{AccessKeyId: dara.String(cred.AccessKeyID), AccessKeySecret: dara.String(cred.AccessKeySecret), Endpoint: dara.String(endpoint), RegionId: dara.String(req.Region), Protocol: dara.String("https"), UserAgent: dara.String("harbormaster"), HttpClient: gate, RetryOptions: &dara.RetryOptions{Retryable: false, MaxAttempts: 1}})
	if err != nil {
		return result, failure("sdk_initialization_failed")
	}
	sdk.DisableSDKError = dara.Bool(true)
	params := &openapi.Params{Action: dara.String("DeleteInstance"), Version: dara.String("2014-05-26"), Protocol: dara.String("HTTPS"), Pathname: dara.String("/"), Method: dara.String("POST"), AuthType: dara.String("AK"), Style: dara.String("RPC"), ReqBodyType: dara.String("formData"), BodyType: dara.String("json")}
	query := map[string]*string{"InstanceId": dara.String(req.InstanceID), "Force": dara.String("false"), "TerminateSubscription": dara.String("false")}
	callCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	response, err := sdk.CallApiWithCtx(callCtx, params, &openapi.OpenApiRequest{Query: query}, &dara.RuntimeOptions{Autoretry: dara.Bool(false), MaxAttempts: dara.Int(1)})
	if !gate.attempted {
		return result, failure("release_request_not_sent")
	}
	result.Outcome = "unknown"
	if err != nil {
		var coded interface{ GetCode() *string }
		if gate.status >= 400 && gate.status < 500 && gate.status != 408 && errors.As(err, &coded) && dara.StringValue(coded.GetCode()) != "" {
			result.Outcome = "rejected"
			return result, failure(releaseErrorCode(dara.StringValue(coded.GetCode())))
		}
		return result, failure("release_outcome_unknown")
	}
	body, ok := response["body"].(map[string]any)
	requestID, _ := body["RequestId"].(string)
	_, hasError := body["Code"]
	if gate.status < 200 || gate.status >= 300 || !ok || strings.TrimSpace(requestID) == "" || hasError {
		return result, failure("release_outcome_unknown")
	}
	return ReleaseResult{Outcome: "accepted"}, nil
}

func releaseErrorCode(code string) string {
	switch strings.ToLower(code) {
	case "incorrectinstancestatus", "incorrectinstancestatus.initializing":
		return "instance_state_conflict"
	case "invalidinstanceid.notfound", "invalidinstanceid.notexist":
		return "instance_not_found"
	case "instancelockedforsecurity":
		return "instance_security_locked"
	case "invalidoperation.deletionprotection":
		return "instance_deletion_protected"
	case "chargetypeviolation":
		return "instance_charge_type_unsupported"
	case "dependencyviolation.slbconfiguring", "dependencyviolation.routeentry", "invalidoperation.enilinked":
		return "instance_dependencies_block_release"
	}
	if category := classify(code); category != "aliyun_unreachable" {
		return category
	}
	return "release_request_rejected"
}
