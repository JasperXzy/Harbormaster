package model

import (
	"math"
	"strconv"
)

type BalanceThreshold struct {
	AccountID string `json:"account_id"`
	Currency  string `json:"currency"`
	Amount    string `json:"amount"`
}

type ProbeThreshold struct {
	PlanID      string   `json:"plan_id"`
	Enabled     *bool    `json:"enabled,omitempty"`
	LatencyMS   *float64 `json:"latency_ms,omitempty"`
	LossPercent *float64 `json:"loss_percent,omitempty"`
}

func NetworkAlertRule(key string) bool {
	return key == "network_latency" || key == "network_loss" || key == "network_unreachable"
}

type AlertParams struct {
	ProbeThresholds     *[]ProbeThreshold   `json:"probe_thresholds,omitempty"`
	DurationSeconds     *int                `json:"duration_seconds,omitempty"`
	RecoverySamples     *int                `json:"recovery_samples,omitempty"`
	BalanceThresholds   *[]BalanceThreshold `json:"balance_thresholds,omitempty"`
	BeforeDays          *int                `json:"before_days,omitempty"`
	ConsecutiveFailures *int                `json:"consecutive_failures,omitempty"`
	OfflineAfterSeconds *int                `json:"offline_after_seconds,omitempty"`
}
type AlertRule struct {
	Key     string      `json:"key"`
	Enabled bool        `json:"enabled"`
	Params  AlertParams `json:"params"`
	Urgent  bool        `json:"urgent"`
}
type AlertRuleUpdate struct {
	Enabled *bool        `json:"enabled"`
	Params  *AlertParams `json:"params"`
}

func (r AlertRule) Validate() map[string]string {
	fields := map[string]string{}
	if !NetworkAlertRule(r.Key) {
		if r.Params.ProbeThresholds != nil {
			fields["params.probe_thresholds"] = "not_applicable"
		}
		if r.Params.RecoverySamples != nil {
			fields["params.recovery_samples"] = "not_applicable"
		}
	}
	if r.Key != "network_latency" && r.Params.DurationSeconds != nil {
		fields["params.duration_seconds"] = "not_applicable"
	}
	if r.Key != "instance_expiring" && r.Params.BeforeDays != nil {
		fields["params.before_days"] = "not_applicable"
	}
	if r.Key != "balance_low" && r.Params.BalanceThresholds != nil {
		fields["params.balance_thresholds"] = "not_applicable"
	}
	switch r.Key {
	case "network_latency", "network_loss", "network_unreachable":
		if r.Params.ProbeThresholds == nil || *r.Params.ProbeThresholds == nil {
			fields["params.probe_thresholds"] = "required"
		} else if len(*r.Params.ProbeThresholds) > 100 {
			fields["params.probe_thresholds"] = "out_of_range"
		} else {
			seen := map[string]bool{}
			for i, v := range *r.Params.ProbeThresholds {
				prefix := "params.probe_thresholds." + strconv.Itoa(i) + "."
				if v.PlanID == "" || len(v.PlanID) > 128 {
					fields[prefix+"plan_id"] = "invalid_format"
				}
				if seen[v.PlanID] {
					fields[prefix+"plan_id"] = "duplicate"
				}
				seen[v.PlanID] = true
				if r.Key == "network_latency" {
					if v.Enabled == nil || *v.Enabled {
						if v.LatencyMS == nil {
							fields[prefix+"latency_ms"] = "required"
						}
					}
				} else if v.LatencyMS != nil {
					fields[prefix+"latency_ms"] = "not_applicable"
				}
				if v.LatencyMS != nil && (math.IsNaN(*v.LatencyMS) || math.IsInf(*v.LatencyMS, 0) || *v.LatencyMS <= 0 || *v.LatencyMS > 60000) {
					fields[prefix+"latency_ms"] = "out_of_range"
				}
				if r.Key != "network_loss" && v.LossPercent != nil {
					fields[prefix+"loss_percent"] = "not_applicable"
				}
				if v.LossPercent != nil && (math.IsNaN(*v.LossPercent) || math.IsInf(*v.LossPercent, 0) || *v.LossPercent < 0 || *v.LossPercent > 100) {
					fields[prefix+"loss_percent"] = "out_of_range"
				}
			}
		}
		if r.Params.RecoverySamples == nil {
			fields["params.recovery_samples"] = "required"
		} else if *r.Params.RecoverySamples < 1 || *r.Params.RecoverySamples > 1000 {
			fields["params.recovery_samples"] = "out_of_range"
		}
		if r.Key == "network_latency" {
			if r.Params.DurationSeconds == nil {
				fields["params.duration_seconds"] = "required"
			} else if *r.Params.DurationSeconds < 60 || *r.Params.DurationSeconds > 86400 {
				fields["params.duration_seconds"] = "out_of_range"
			}
		}
		if r.Key == "network_unreachable" {
			if r.Params.ConsecutiveFailures == nil {
				fields["params.consecutive_failures"] = "required"
			} else if *r.Params.ConsecutiveFailures < 1 || *r.Params.ConsecutiveFailures > 1000 {
				fields["params.consecutive_failures"] = "out_of_range"
			}
		} else if r.Params.ConsecutiveFailures != nil {
			fields["params.consecutive_failures"] = "not_applicable"
		}
		if r.Params.OfflineAfterSeconds != nil {
			fields["params.offline_after_seconds"] = "not_applicable"
		}
	case "balance_low":
		if r.Params.BalanceThresholds == nil || *r.Params.BalanceThresholds == nil {
			fields["params.balance_thresholds"] = "required"
		} else if len(*r.Params.BalanceThresholds) > 100 {
			fields["params.balance_thresholds"] = "out_of_range"
		} else {
			seen := map[string]bool{}
			for index, threshold := range *r.Params.BalanceThresholds {
				prefix := "params.balance_thresholds." + strconv.Itoa(index) + "."
				if threshold.AccountID == "" || len(threshold.AccountID) > 128 {
					fields[prefix+"account_id"] = "invalid_format"
				}
				if seen[threshold.AccountID] {
					fields[prefix+"account_id"] = "duplicate"
				}
				seen[threshold.AccountID] = true
				if !currencyCode.MatchString(threshold.Currency) {
					fields[prefix+"currency"] = "invalid_format"
				}
				if _, ok := DecimalAmount(threshold.Amount); !ok {
					fields[prefix+"amount"] = "invalid_format"
				}
			}
		}
		if r.Params.ConsecutiveFailures != nil {
			fields["params.consecutive_failures"] = "not_applicable"
		}
		if r.Params.OfflineAfterSeconds != nil {
			fields["params.offline_after_seconds"] = "not_applicable"
		}
	case "spec_bandwidth_insufficient", "instance_public_ip_changed", "network_route_change":
		if r.Params.ConsecutiveFailures != nil {
			fields["params.consecutive_failures"] = "not_applicable"
		}
		if r.Params.OfflineAfterSeconds != nil {
			fields["params.offline_after_seconds"] = "not_applicable"
		}
	case "instance_expiring":
		if r.Params.BeforeDays == nil {
			fields["params.before_days"] = "required"
		} else if *r.Params.BeforeDays < 0 || *r.Params.BeforeDays > 365 {
			fields["params.before_days"] = "out_of_range"
		}
		if r.Params.ConsecutiveFailures != nil {
			fields["params.consecutive_failures"] = "not_applicable"
		}
		if r.Params.OfflineAfterSeconds != nil {
			fields["params.offline_after_seconds"] = "not_applicable"
		}
	case "account_api_failure", "cdt_official_unavailable":
		if r.Params.ConsecutiveFailures == nil {
			fields["params.consecutive_failures"] = "required"
		} else if *r.Params.ConsecutiveFailures < 1 || *r.Params.ConsecutiveFailures > 1000 {
			fields["params.consecutive_failures"] = "out_of_range"
		}
		if r.Params.OfflineAfterSeconds != nil {
			fields["params.offline_after_seconds"] = "not_applicable"
		}
	case "agent_offline":
		if r.Params.OfflineAfterSeconds == nil {
			fields["params.offline_after_seconds"] = "required"
		} else if *r.Params.OfflineAfterSeconds < 10 || *r.Params.OfflineAfterSeconds > 86400 {
			fields["params.offline_after_seconds"] = "out_of_range"
		}
		if r.Params.ConsecutiveFailures != nil {
			fields["params.consecutive_failures"] = "not_applicable"
		}
	default:
		fields["key"] = "not_supported"
	}
	return fields
}
