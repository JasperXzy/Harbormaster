package model

import (
	"regexp"
	"slices"
	"strings"
	"time"
)

var telegramUserIDPattern = regexp.MustCompile(`^[1-9][0-9]{0,19}$`)

type TelegramControlSettings struct {
	Revision       int64      `json:"revision"`
	Enabled        bool       `json:"enabled"`
	AllowedUserIDs []string   `json:"allowed_user_ids"`
	UpdatedAt      *time.Time `json:"updated_at"`
}

func DefaultTelegramControlSettings() TelegramControlSettings {
	return TelegramControlSettings{AllowedUserIDs: []string{}}
}

func (c *TelegramControlSettings) NormalizeAndValidate() map[string]string {
	fields := map[string]string{}
	if c.Revision < 0 {
		fields["revision"] = "out_of_range"
	}
	if c.AllowedUserIDs == nil {
		fields["allowed_user_ids"] = "required"
		return fields
	}
	if len(c.AllowedUserIDs) > 50 {
		fields["allowed_user_ids"] = "out_of_range"
		return fields
	}
	seen := map[string]bool{}
	clean := make([]string, 0, len(c.AllowedUserIDs))
	for _, raw := range c.AllowedUserIDs {
		id := strings.TrimSpace(raw)
		if !telegramUserIDPattern.MatchString(id) {
			fields["allowed_user_ids"] = "invalid_format"
			return fields
		}
		if !seen[id] {
			seen[id] = true
			clean = append(clean, id)
		}
	}
	slices.Sort(clean)
	c.AllowedUserIDs = clean
	if c.Enabled && len(c.AllowedUserIDs) == 0 {
		fields["allowed_user_ids"] = "required_when_enabled"
	}
	return fields
}

type WidgetSummary struct {
	UpdatedAt    time.Time            `json:"updated_at"`
	PowerEnabled bool                 `json:"power_enabled"`
	Accounts     WidgetAccountCounts  `json:"accounts"`
	Instances    WidgetInstanceCounts `json:"instances"`
	Agents       WidgetAgentCounts    `json:"agents"`
	Quotas       []WidgetQuotaSummary `json:"quotas"`
}

type WidgetAccountCounts struct {
	Total   int `json:"total"`
	Enabled int `json:"enabled"`
	Failing int `json:"failing"`
}

type WidgetInstanceCounts struct {
	Total       int `json:"total"`
	Running     int `json:"running"`
	Stopped     int `json:"stopped"`
	Other       int `json:"other"`
	Unprotected int `json:"unprotected"`
}

type WidgetAgentCounts struct {
	Online       int `json:"online"`
	Offline      int `json:"offline"`
	NotInstalled int `json:"not_installed"`
}

type WidgetQuotaSummary struct {
	AccountID             string     `json:"account_id"`
	AccountName           string     `json:"account_name"`
	Scope                 string     `json:"scope"`
	QuotaBytes            int64      `json:"quota_bytes"`
	OfficialBytes         *int64     `json:"official_bytes"`
	EstimatedBytes        *int64     `json:"estimated_bytes"`
	RemainingBytes        *int64     `json:"remaining_bytes"`
	Unverified            bool       `json:"unverified"`
	OverageCost           *string    `json:"overage_cost"`
	ForecastCycleEndBytes *int64     `json:"forecast_cycle_end_bytes"`
	UpdatedAt             *time.Time `json:"updated_at"`
}
