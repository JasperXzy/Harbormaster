package model

import (
	"crypto/rand"
	"encoding/hex"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

type APIError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func Error(code string) *APIError { return &APIError{code, code} }

type PermissionItem struct {
	Key    string `json:"key"`
	Result string `json:"result"`
}
type PermissionCheck struct {
	CheckedAt time.Time        `json:"checked_at"`
	Items     []PermissionItem `json:"items"`
}
type Account struct {
	ID                  string           `json:"id"`
	Name                string           `json:"name"`
	Site                string           `json:"site"`
	AccessKeyIDMasked   string           `json:"access_key_id_masked"`
	RegionIDs           []string         `json:"region_ids"`
	Enabled             bool             `json:"enabled"`
	QuotaMainland       int64            `json:"quota_mainland_bytes"`
	QuotaOverseas       int64            `json:"quota_overseas_bytes"`
	ResetDay            int              `json:"reset_day"`
	Thresholds          []int            `json:"alert_thresholds_percent"`
	SyncStatus          string           `json:"sync_status"`
	LastSyncAt          *time.Time       `json:"last_sync_at"`
	LastError           *APIError        `json:"last_error"`
	ConsecutiveFailures int              `json:"consecutive_failures"`
	PermissionCheck     *PermissionCheck `json:"permission_check"`
	InstanceCount       int              `json:"instance_count"`
	UpdatedAt           time.Time        `json:"updated_at"`
	Revision            int64            `json:"-"`
	CredentialVersion   int64            `json:"-"`
}

// Credential is never an API response or job payload. Both fields are encrypted.
type Credential struct {
	AccessKeyID     string `json:"access_key_id"`
	AccessKeySecret string `json:"access_key_secret"`
}
type AccountCreate struct {
	Name string `json:"name"`
	Site string `json:"site"`
	Credential
	RegionIDs     []string `json:"region_ids"`
	QuotaMainland *int64   `json:"quota_mainland_bytes"`
	QuotaOverseas *int64   `json:"quota_overseas_bytes"`
	ResetDay      *int     `json:"reset_day"`
	Thresholds    *[]int   `json:"alert_thresholds_percent"`
}
type AccountUpdate struct {
	Name            *string   `json:"name"`
	AccessKeyID     *string   `json:"access_key_id"`
	AccessKeySecret *string   `json:"access_key_secret"`
	RegionIDs       *[]string `json:"region_ids"`
	Enabled         *bool     `json:"enabled"`
	QuotaMainland   *int64    `json:"quota_mainland_bytes"`
	QuotaOverseas   *int64    `json:"quota_overseas_bytes"`
	ResetDay        *int      `json:"reset_day"`
	Thresholds      *[]int    `json:"alert_thresholds_percent"`
}

var RegionPattern = regexp.MustCompile(`^[a-z][a-z0-9]*(?:-[a-z0-9]+)+$`)
var keyIDPattern = regexp.MustCompile(`^[a-zA-Z0-9]{8,256}$`)

func (c Credential) Validate() map[string]string {
	f := map[string]string{}
	if !keyIDPattern.MatchString(c.AccessKeyID) {
		f["access_key_id"] = "invalid_format"
	}
	if len(c.AccessKeySecret) < 16 || len(c.AccessKeySecret) > 512 || strings.IndexFunc(c.AccessKeySecret, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
		f["access_key_secret"] = "invalid_format"
	}
	return f
}
func (a Account) Validate() map[string]string {
	f := map[string]string{}
	if len(a.Name) == 0 || utf8.RuneCountInString(a.Name) > 100 || strings.IndexFunc(a.Name, unicode.IsControl) >= 0 {
		f["name"] = "invalid_format"
	}
	if a.Site != "china" && a.Site != "international" {
		f["site"] = "invalid_format"
	}
	seen := map[string]bool{}
	if len(a.RegionIDs) == 0 || len(a.RegionIDs) > 20 {
		f["region_ids"] = "out_of_range"
	}
	for _, r := range a.RegionIDs {
		if len(r) > 64 || !RegionPattern.MatchString(r) || seen[r] {
			f["region_ids"] = "invalid_format"
		}
		seen[r] = true
	}
	for k, v := range map[string]int64{"quota_mainland_bytes": a.QuotaMainland, "quota_overseas_bytes": a.QuotaOverseas} {
		if v < 0 || v > 1<<50 {
			f[k] = "out_of_range"
		}
	}
	if a.ResetDay != 1 {
		f["reset_day"] = "out_of_range"
	}
	if len(a.Thresholds) == 0 || len(a.Thresholds) > 20 {
		f["alert_thresholds_percent"] = "out_of_range"
	}
	previous := 0
	for _, v := range a.Thresholds {
		if v <= previous || v > 100 {
			f["alert_thresholds_percent"] = "out_of_range"
		}
		previous = v
	}
	return f
}
func (u AccountUpdate) Apply(a *Account) {
	if u.Name != nil {
		a.Name = strings.TrimSpace(*u.Name)
	}
	if u.RegionIDs != nil {
		a.RegionIDs = *u.RegionIDs
	}
	if u.Enabled != nil {
		a.Enabled = *u.Enabled
	}
	if u.QuotaMainland != nil {
		a.QuotaMainland = *u.QuotaMainland
	}
	if u.QuotaOverseas != nil {
		a.QuotaOverseas = *u.QuotaOverseas
	}
	if u.ResetDay != nil {
		a.ResetDay = *u.ResetDay
	}
	if u.Thresholds != nil {
		a.Thresholds = *u.Thresholds
	}
}
func NewID(prefix string) string {
	var b [16]byte
	// crypto/rand.Read terminates the process on an entropy-source failure.
	_, _ = rand.Read(b[:])
	return prefix + hex.EncodeToString(b[:])
}
func CredentialOwner(id string) string { return "account/" + id + "/access-key" }

type Job struct {
	ID         string     `json:"id"`
	Type       string     `json:"type"`
	Status     string     `json:"status"`
	CreatedAt  time.Time  `json:"created_at"`
	StartedAt  *time.Time `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at"`
	Error      *APIError  `json:"error"`
	Result     any        `json:"result"`
	AccountIDs []string   `json:"account_ids"`
	InstanceID *string    `json:"instance_id"`
	Trigger    string     `json:"trigger"`
	Sequence   int64      `json:"-"`
	Owner      string     `json:"-"`
	Attempts   int        `json:"-"`
	Scope      string     `json:"-"`
}
type Snapshot struct {
	Kind    string
	ID      string
	Region  string
	Payload map[string]any
	// True only when all IPv4 sources for this instance were read completely.
	// Used by event comparisons; it is not a claim about CDT eligibility.
	IPv4Complete bool
}
type ScanResult struct {
	Snapshots   []Snapshot
	Finance     []FinanceObservation
	SWASTraffic []SWASTrafficObservation
	// Only completely read scopes may hide old, now-absent resources.
	CompleteScopes map[string]bool
	Permissions    *PermissionCheck
	Failure        string
	CDT            *CDTObservation
}

func Scope(kind, region string) string { return kind + ":" + region }
