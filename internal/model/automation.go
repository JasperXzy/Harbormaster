package model

import (
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

var civilTimePattern = regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]$`)

type PowerScheduleInput struct {
	Revision        int64    `json:"revision"`
	Name            string   `json:"name"`
	Enabled         bool     `json:"enabled"`
	AccountID       string   `json:"account_id"`
	Action          string   `json:"action"`
	StopMode        string   `json:"stop_mode"`
	TimeOfDay       string   `json:"time_of_day"`
	DaysOfWeek      []int    `json:"days_of_week"`
	CatchUpMinutes  int      `json:"catch_up_minutes"`
	Targets         []string `json:"target_instance_ids"`
	DryRun          bool     `json:"dry_run"`
	ConfirmedSelfID string   `json:"confirmed_self_instance_id"`
}

type PowerSchedule struct {
	PowerScheduleInput
	ID              string     `json:"id"`
	ActivatedAt     time.Time  `json:"activated_at"`
	LastTriggeredAt *time.Time `json:"last_triggered_at"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

func (in *PowerScheduleInput) NormalizeAndValidate() bool {
	if in.Revision < 0 || !utf8.ValidString(in.Name) || strings.TrimSpace(in.Name) == "" || utf8.RuneCountInString(in.Name) > 80 || strings.ContainsAny(in.Name, "\r\n") {
		return false
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.AccountID == "" || (in.Action != "start" && in.Action != "stop") {
		return false
	}
	if in.Action == "start" && in.StopMode != "" {
		return false
	}
	if in.Action == "stop" && in.StopMode != "" && in.StopMode != "keep_charging" && in.StopMode != "stop_charging" {
		return false
	}
	if !civilTimePattern.MatchString(in.TimeOfDay) {
		return false
	}
	if len(in.DaysOfWeek) == 0 || len(in.DaysOfWeek) > 7 {
		return false
	}
	days := slices.Clone(in.DaysOfWeek)
	slices.Sort(days)
	for i, d := range days {
		if d < 0 || d > 6 || (i > 0 && d == days[i-1]) {
			return false
		}
	}
	in.DaysOfWeek = days
	if in.CatchUpMinutes < 0 || in.CatchUpMinutes > 180 {
		return false
	}
	if len(in.Targets) == 0 || len(in.Targets) > 1000 {
		return false
	}
	targets := slices.Clone(in.Targets)
	slices.Sort(targets)
	for i, id := range targets {
		if id == "" || (i > 0 && id == targets[i-1]) {
			return false
		}
	}
	in.Targets = targets
	if in.Action == "stop" && in.ConfirmedSelfID != "" && !slices.Contains(in.Targets, in.ConfirmedSelfID) {
		return false
	}
	if in.Action == "start" && in.ConfirmedSelfID != "" {
		return false
	}
	return true
}

type KeepaliveSettings struct {
	Revision        int64      `json:"revision"`
	Enabled         bool       `json:"enabled"`
	DryRun          bool       `json:"dry_run"`
	SpotAutoRecover bool       `json:"spot_auto_recover"`
	Targets         []string   `json:"target_instance_ids"`
	UpdatedAt       *time.Time `json:"updated_at"`
}

func DefaultKeepaliveSettings() KeepaliveSettings {
	return KeepaliveSettings{SpotAutoRecover: true, Targets: []string{}}
}

type MonthlyResetSettings struct {
	Revision       int64      `json:"revision"`
	Enabled        bool       `json:"enabled"`
	DryRun         bool       `json:"dry_run"`
	Targets        []string   `json:"target_instance_ids"`
	LastCycleStart *time.Time `json:"last_cycle_start"`
	UpdatedAt      *time.Time `json:"updated_at"`
}

func DefaultMonthlyResetSettings() MonthlyResetSettings {
	return MonthlyResetSettings{Targets: []string{}}
}

type PendingIntentConfirmation struct {
	DetectedAt time.Time `json:"detected_at"`
}
