package aliyun

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
	openapi "github.com/alibabacloud-go/darabonba-openapi/v2/client"
	"github.com/alibabacloud-go/tea/dara"
)

type AllocatedEIP struct {
	AllocationID string
	EIPAddress   string
}

type CreatedNetwork struct {
	VPCID           string
	VSwitchID       string
	SecurityGroupID string
}

type M4Cloud interface {
	ECSCatalog(ctx context.Context, cred Credentials, accountID, region string) (model.ECSCatalog, error)
	AllocateEIP(ctx context.Context, cred Credentials, region string, bandwidthMbps int, chargeType string) (AllocatedEIP, error)
	AddEIPToBandwidthPackage(ctx context.Context, cred Credentials, region, packageID, allocationID string) error
	RemoveEIPFromBandwidthPackage(ctx context.Context, cred Credentials, region, packageID, allocationID string) error
	AssociateEIP(ctx context.Context, cred Credentials, region, allocationID, instanceID string) error
	UnassociateEIP(ctx context.Context, cred Credentials, region, allocationID, instanceID string) error
	ReleaseEIP(ctx context.Context, cred Credentials, region, allocationID string) error
	EnsureNetwork(ctx context.Context, cred Credentials, region, zoneID string) (CreatedNetwork, error)
	RunECSInstance(ctx context.Context, cred Credentials, in model.ECSCreateInput, vpcID, vswitchID, sgID string) (string, error)
}

type M4Client struct {
	client *Client
}

func NewM4Client(transport http.RoundTripper, interval time.Duration) *M4Client {
	return &M4Client{client: NewClient(transport, interval)}
}

func (c *M4Client) rpcCall(ctx context.Context, cred Credentials, product, version, method, action, region string, query map[string]string) (map[string]any, error) {
	if debugAtStartup || os.Getenv("DEBUG") != "" {
		return nil, failure("sdk_debug_must_be_disabled")
	}
	if cred.Site != "china" && cred.Site != "international" {
		return nil, failure("invalid_site")
	}
	if len(region) > 64 || !model.RegionPattern.MatchString(region) {
		return nil, failure("invalid_region")
	}
	if err := c.client.wait(ctx); err != nil {
		return nil, failure("cancelled")
	}
	endpoint := Endpoint(product, region, cred.Site)
	var httpClient dara.HttpClient
	rg := readHTTP{client: c.client.http, endpoint: endpoint, action: action, version: version, method: method}
	if method == "GET" {
		httpClient = rg
	} else {
		httpClient = &writeHTTP{gate: rg}
	}
	sdk, err := openapi.NewClient(&openapi.Config{
		AccessKeyId:     dara.String(cred.AccessKeyID),
		AccessKeySecret: dara.String(cred.AccessKeySecret),
		Endpoint:        dara.String(endpoint),
		RegionId:        dara.String(region),
		Protocol:        dara.String("https"),
		UserAgent:       dara.String("harbormaster"),
		HttpClient:      httpClient,
		RetryOptions:    &dara.RetryOptions{Retryable: false, MaxAttempts: 1},
	})
	if err != nil {
		return nil, failure("sdk_initialization_failed")
	}
	sdk.DisableSDKError = dara.Bool(true)
	params := &openapi.Params{
		Action:      dara.String(action),
		Version:     dara.String(version),
		Protocol:    dara.String("HTTPS"),
		Pathname:    dara.String("/"),
		Method:      dara.String(method),
		AuthType:    dara.String("AK"),
		Style:       dara.String("RPC"),
		ReqBodyType: dara.String("formData"),
		BodyType:    dara.String("json"),
	}
	q := make(map[string]*string, len(query))
	for k, v := range query {
		q[k] = dara.String(v)
	}
	callCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	resp, err := sdk.CallApiWithCtx(callCtx, params, &openapi.OpenApiRequest{Query: q}, &dara.RuntimeOptions{Autoretry: dara.Bool(false), MaxAttempts: dara.Int(1)})
	if err != nil {
		var coded interface{ GetCode() *string }
		if errors.As(err, &coded) && dara.StringValue(coded.GetCode()) != "" {
			return nil, failure(classify(dara.StringValue(coded.GetCode())))
		}
		return nil, failure("aliyun_unreachable")
	}
	body, ok := resp["body"].(map[string]any)
	if !ok {
		return nil, failure("invalid_response")
	}
	if code, hasErr := body["Code"].(string); hasErr && code != "" {
		return nil, failure(classify(code))
	}
	return body, nil
}

func (c *M4Client) ECSCatalog(ctx context.Context, cred Credentials, accountID, region string) (model.ECSCatalog, error) {
	cat := model.ECSCatalog{
		AccountID: accountID,
		RegionID:  region,
		Zones:     []model.ECSCatalogZone{},
		Images:    []model.ECSCatalogImage{},
		InstanceTypes: []model.ECSCatalogInstanceType{
			{ID: "ecs.e-c1m1.large", CPU: 2, MemoryGiB: 2},
			{ID: "ecs.e-c1m2.large", CPU: 2, MemoryGiB: 4},
			{ID: "ecs.e-c4m1.large", CPU: 2, MemoryGiB: 0.5},
			{ID: "ecs.t6-c1m1.large", CPU: 2, MemoryGiB: 2},
			{ID: "ecs.t6-c2m1.large", CPU: 2, MemoryGiB: 1},
			{ID: "ecs.u1-c1m2.large", CPU: 2, MemoryGiB: 4},
		},
		VPCs:      []model.ECSCatalogVPC{},
		VSwitches: []model.ECSCatalogVSwitch{},
	}
	if zBody, err := c.rpcCall(ctx, cred, "ecs", "2014-05-26", "GET", "DescribeZones", region, map[string]string{"RegionId": region}); err == nil {
		if val, ok := valueAt(zBody, "Zones.Zone"); ok {
			if rows, ok := objects(val); ok {
				for _, r := range rows {
					id := text(r, "ZoneId")
					name := text(r, "LocalName")
					if name == "" {
						name = id
					}
					if id != "" {
						cat.Zones = append(cat.Zones, model.ECSCatalogZone{ID: id, Name: name})
					}
				}
			}
		}
	} else {
		return cat, err
	}
	if imgBody, err := c.rpcCall(ctx, cred, "ecs", "2014-05-26", "GET", "DescribeImages", region, map[string]string{"RegionId": region, "ImageOwnerAlias": "system", "Status": "Available", "PageSize": "30"}); err == nil {
		if val, ok := valueAt(imgBody, "Images.Image"); ok {
			if rows, ok := objects(val); ok {
				for _, r := range rows {
					id := text(r, "ImageId")
					name := text(r, "OSName")
					if name == "" {
						name = id
					}
					if id != "" {
						cat.Images = append(cat.Images, model.ECSCatalogImage{ID: id, Name: name})
					}
				}
			}
		}
	}
	if vpcBody, err := c.rpcCall(ctx, cred, "vpc", "2016-04-28", "GET", "DescribeVpcs", region, map[string]string{"RegionId": region, "PageSize": "50"}); err == nil {
		if val, ok := valueAt(vpcBody, "Vpcs.Vpc"); ok {
			if rows, ok := objects(val); ok {
				for _, r := range rows {
					id := text(r, "VpcId")
					if id != "" {
						cat.VPCs = append(cat.VPCs, model.ECSCatalogVPC{ID: id, Name: text(r, "VpcName"), CidrBlock: text(r, "CidrBlock")})
					}
				}
			}
		}
	}
	if vswBody, err := c.rpcCall(ctx, cred, "vpc", "2016-04-28", "GET", "DescribeVSwitches", region, map[string]string{"RegionId": region, "PageSize": "50"}); err == nil {
		if val, ok := valueAt(vswBody, "VSwitches.VSwitch"); ok {
			if rows, ok := objects(val); ok {
				for _, r := range rows {
					id := text(r, "VSwitchId")
					if id != "" {
						cat.VSwitches = append(cat.VSwitches, model.ECSCatalogVSwitch{
							ID: id, VPCID: text(r, "VpcId"), ZoneID: text(r, "ZoneId"),
							Name: text(r, "VSwitchName"), CidrBlock: text(r, "CidrBlock"),
						})
					}
				}
			}
		}
	}
	return cat, nil
}

func (c *M4Client) AllocateEIP(ctx context.Context, cred Credentials, region string, bandwidthMbps int, chargeType string) (AllocatedEIP, error) {
	body, err := c.rpcCall(ctx, cred, "vpc", "2016-04-28", "POST", "AllocateEipAddress", region, map[string]string{
		"RegionId":           region,
		"Bandwidth":          strconv.Itoa(bandwidthMbps),
		"InternetChargeType": chargeType,
		"InstanceChargeType": "PostPaid",
	})
	if err != nil {
		return AllocatedEIP{}, err
	}
	allocID := text(body, "AllocationId")
	ip := text(body, "EipAddress")
	if allocID == "" || ip == "" {
		return AllocatedEIP{}, failure("invalid_response")
	}
	return AllocatedEIP{AllocationID: allocID, EIPAddress: ip}, nil
}

func (c *M4Client) AddEIPToBandwidthPackage(ctx context.Context, cred Credentials, region, packageID, allocationID string) error {
	_, err := c.rpcCall(ctx, cred, "vpc", "2016-04-28", "POST", "AddCommonBandwidthPackageIp", region, map[string]string{
		"RegionId":           region,
		"BandwidthPackageId": packageID,
		"IpInstanceId":       allocationID,
	})
	return err
}

func (c *M4Client) RemoveEIPFromBandwidthPackage(ctx context.Context, cred Credentials, region, packageID, allocationID string) error {
	_, err := c.rpcCall(ctx, cred, "vpc", "2016-04-28", "POST", "RemoveCommonBandwidthPackageIp", region, map[string]string{
		"RegionId":           region,
		"BandwidthPackageId": packageID,
		"IpInstanceId":       allocationID,
	})
	return err
}

func (c *M4Client) AssociateEIP(ctx context.Context, cred Credentials, region, allocationID, instanceID string) error {
	_, err := c.rpcCall(ctx, cred, "vpc", "2016-04-28", "POST", "AssociateEipAddress", region, map[string]string{
		"RegionId":     region,
		"AllocationId": allocationID,
		"InstanceId":   instanceID,
		"InstanceType": "EcsInstance",
	})
	return err
}

func (c *M4Client) UnassociateEIP(ctx context.Context, cred Credentials, region, allocationID, instanceID string) error {
	_, err := c.rpcCall(ctx, cred, "vpc", "2016-04-28", "POST", "UnassociateEipAddress", region, map[string]string{
		"RegionId":     region,
		"AllocationId": allocationID,
		"InstanceId":   instanceID,
		"InstanceType": "EcsInstance",
	})
	return err
}

func (c *M4Client) ReleaseEIP(ctx context.Context, cred Credentials, region, allocationID string) error {
	_, err := c.rpcCall(ctx, cred, "vpc", "2016-04-28", "POST", "ReleaseEipAddress", region, map[string]string{
		"RegionId":     region,
		"AllocationId": allocationID,
	})
	return err
}

func (c *M4Client) EnsureNetwork(ctx context.Context, cred Credentials, region, zoneID string) (CreatedNetwork, error) {
	var netOut CreatedNetwork
	vpcBody, err := c.rpcCall(ctx, cred, "vpc", "2016-04-28", "POST", "CreateVpc", region, map[string]string{
		"RegionId":  region,
		"VpcName":   "harbormaster-vpc",
		"CidrBlock": "172.16.0.0/16",
	})
	if err != nil {
		return netOut, err
	}
	netOut.VPCID = text(vpcBody, "VpcId")
	if netOut.VPCID == "" {
		return netOut, failure("invalid_response")
	}
	// Brief wait for VPC provisioning
	select {
	case <-ctx.Done():
		return netOut, failure("cancelled")
	case <-time.After(3 * time.Second):
	}
	vswBody, err := c.rpcCall(ctx, cred, "vpc", "2016-04-28", "POST", "CreateVSwitch", region, map[string]string{
		"RegionId":    region,
		"ZoneId":      zoneID,
		"VpcId":       netOut.VPCID,
		"VSwitchName": "harbormaster-vsw",
		"CidrBlock":   "172.16.1.0/24",
	})
	if err != nil {
		return netOut, err
	}
	netOut.VSwitchID = text(vswBody, "VSwitchId")
	if netOut.VSwitchID == "" {
		return netOut, failure("invalid_response")
	}
	sgBody, err := c.rpcCall(ctx, cred, "ecs", "2014-05-26", "POST", "CreateSecurityGroup", region, map[string]string{
		"RegionId":          region,
		"VpcId":             netOut.VPCID,
		"SecurityGroupName": "harbormaster-sg",
	})
	if err != nil {
		return netOut, err
	}
	netOut.SecurityGroupID = text(sgBody, "SecurityGroupId")
	if netOut.SecurityGroupID == "" {
		return netOut, failure("invalid_response")
	}
	return netOut, nil
}

func (c *M4Client) RunECSInstance(ctx context.Context, cred Credentials, in model.ECSCreateInput, vpcID, vswitchID, sgID string) (string, error) {
	if sgID == "" {
		sgBody, err := c.rpcCall(ctx, cred, "ecs", "2014-05-26", "POST", "CreateSecurityGroup", in.RegionID, map[string]string{
			"RegionId":          in.RegionID,
			"VpcId":             vpcID,
			"SecurityGroupName": "harbormaster-sg",
		})
		if err != nil {
			return "", err
		}
		sgID = text(sgBody, "SecurityGroupId")
	}
	chargeType := "PostPaid"
	spotStrategy := "NoSpot"
	if in.InstanceChargeType == "Spot" {
		spotStrategy = "SpotAsPriceGo"
	}
	q := map[string]string{
		"RegionId":                in.RegionID,
		"ZoneId":                  in.ZoneID,
		"ImageId":                 in.ImageID,
		"InstanceType":            in.InstanceType,
		"SecurityGroupId":         sgID,
		"VSwitchId":               vswitchID,
		"InstanceName":            strings.TrimSpace(in.InstanceName),
		"InstanceChargeType":      chargeType,
		"SpotStrategy":            spotStrategy,
		"InternetChargeType":      in.InternetChargeType,
		"InternetMaxBandwidthOut": strconv.Itoa(in.BandwidthOutMbps),
		"SystemDisk.Category":     "cloud_essd",
		"SystemDisk.Size":         strconv.Itoa(in.SystemDiskGiB),
		"Amount":                  "1",
	}
	body, err := c.rpcCall(ctx, cred, "ecs", "2014-05-26", "POST", "RunInstances", in.RegionID, q)
	if err != nil {
		return "", err
	}
	if val, ok := valueAt(body, "InstanceIdSets.InstanceIdSet"); ok {
		if arr, ok := val.([]any); ok && len(arr) > 0 {
			if id, ok := arr[0].(string); ok && id != "" {
				return id, nil
			}
		}
	}
	return "", failure("invalid_response")
}
