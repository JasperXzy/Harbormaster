package model

import (
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

type APIKey struct {
	Sequence   int64      `json:"-"`
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Scopes     []string   `json:"scopes"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
	RevokedAt  *time.Time `json:"revoked_at"`
}
type APIKeyCreate struct {
	Name   string   `json:"name"`
	Scopes []string `json:"scopes"`
}

func ValidAPIKeyScope(scope string) bool {
	switch scope {
	case "summary:read", "instance:control", "cron:run":
		return true
	default:
		return false
	}
}
func (in APIKeyCreate) Validate() map[string]string {
	fields := map[string]string{}
	name := strings.TrimSpace(in.Name)
	if !utf8.ValidString(name) || utf8.RuneCountInString(name) < 1 || utf8.RuneCountInString(name) > 80 || strings.ContainsFunc(name, unicode.IsControl) {
		fields["name"] = "invalid_format"
	}
	if len(in.Scopes) < 1 || len(in.Scopes) > 3 {
		fields["scopes"] = "out_of_range"
	}
	seen := map[string]bool{}
	for _, scope := range in.Scopes {
		if !ValidAPIKeyScope(scope) || seen[scope] {
			fields["scopes"] = "invalid_choice"
		}
		seen[scope] = true
	}
	return fields
}
