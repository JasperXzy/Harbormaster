// Package rampolicy supplies policy text only. It has no credentials, cloud
// client, policy attachment or resource execution capability.
package rampolicy

type Statement struct {
	Effect   string   `json:"Effect"`
	Action   []string `json:"Action"`
	Resource string   `json:"Resource"`
}
type Document struct {
	Version   string      `json:"Version"`
	Statement []Statement `json:"Statement"`
}
type Draft struct {
	ID                    string   `json:"id"`
	ReviewedOn            string   `json:"reviewed_on"`
	Verification          string   `json:"verification"`
	CloudWritePermissions bool     `json:"cloud_write_permissions"`
	Limitations           []string `json:"limitations"`
	Document              Document `json:"document"`
}

// Catalog returns fresh documents so callers cannot alter subsequent responses.
// Generated deployment copies can be refreshed with:
// go run ./internal/rampolicy/export ./deploy/ram
func Catalog() []Draft {
	read := Statement{Effect: "Allow", Resource: "*", Action: []string{
		"ecs:DescribeInstances", "ecs:DescribeNetworkInterfaces", "ecs:DescribeInstanceAutoRenewAttribute",
		"vpc:DescribeEipAddresses", "vpc:DescribeCommonBandwidthPackages",
		"swas-open:ListInstances", "swas-open:ListInstancesTrafficPackages", "swas-open:DescribeMonitorData",
		"cms:QueryMetricList", "bss:DescribeAcccount", "bss:DescribeBillList", "bss:DescribeInstances",
	}}
	base := []string{"cdt_action_unverified", "public_metadata_actions_unverified", "resource_scope_not_account_bound"}
	readonly := Draft{ID: "readonly", ReviewedOn: "2026-11-12", Verification: "draft", Limitations: append([]string{}, base...), Document: Document{Version: "1", Statement: []Statement{read}}}
	power := Draft{ID: "instance_power", ReviewedOn: "2026-11-12", Verification: "draft", CloudWritePermissions: true, Limitations: append(append([]string{}, base...), "runtime_writes_require_explicit_enable"), Document: Document{Version: "1", Statement: []Statement{
		read,
		{Effect: "Allow", Resource: "acs:ecs:*:*:instance/*", Action: []string{"ecs:StartInstance", "ecs:StopInstance", "ecs:RebootInstance"}},
		{Effect: "Allow", Resource: "acs:swas-open:*:*:instance/*", Action: []string{"swas-open:StartInstance", "swas-open:StopInstance", "swas-open:RebootInstance"}},
	}}}
	manage := Draft{ID: "resource_management", ReviewedOn: "2026-11-12", Verification: "draft", CloudWritePermissions: true, Limitations: append(append([]string{}, power.Limitations...), "creation_dependencies_pending"), Document: Document{Version: "1", Statement: append([]Statement{}, power.Document.Statement...)}}
	manage.Document.Statement = append(manage.Document.Statement,
		Statement{Effect: "Allow", Resource: "acs:ecs:*:*:instance/*", Action: []string{"ecs:DeleteInstance"}},
		Statement{Effect: "Allow", Resource: "*", Action: []string{"ecs:RunInstances", "vpc:AllocateEipAddress", "vpc:AssociateEipAddress", "vpc:UnassociateEipAddress", "vpc:ReleaseEipAddress", "vpc:CreateVpc", "vpc:CreateVSwitch"}},
	)
	return []Draft{readonly, power, manage}
}
