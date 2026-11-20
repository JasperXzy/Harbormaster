package model

import (
	"strings"
	"time"
	"unicode/utf8"
)

type InstanceMetadata struct {
	InstanceID string     `json:"instance_id"`
	Note       string     `json:"note"`
	Group      *string    `json:"group"`
	Tags       []string   `json:"tags"`
	UpdatedAt  *time.Time `json:"updated_at"`
}

type InstanceMetadataUpdate struct {
	Note     *string   `json:"note"`
	Group    *string   `json:"group"`
	GroupSet bool      `json:"-"`
	Tags     *[]string `json:"tags"`
}

func NormalizeInstanceMetadata(current InstanceMetadata, in InstanceMetadataUpdate) (InstanceMetadata, map[string]string) {
	fields := map[string]string{}
	out := current
	if out.Tags == nil {
		out.Tags = []string{}
	}
	if in.Note != nil {
		if !utf8.ValidString(*in.Note) {
			fields["note"] = "invalid_format"
		} else {
			norm := strings.ReplaceAll(*in.Note, "\r\n", "\n")
			norm = strings.ReplaceAll(norm, "\r", "\n")
			norm = strings.TrimSpace(norm)
			if utf8.RuneCountInString(norm) > 2000 {
				fields["note"] = "out_of_range"
			} else {
				out.Note = norm
			}
		}
	}
	if in.GroupSet {
		if in.Group == nil {
			out.Group = nil
		} else if !utf8.ValidString(*in.Group) || strings.ContainsAny(*in.Group, "\r\n") {
			fields["group"] = "invalid_format"
		} else {
			g := strings.TrimSpace(*in.Group)
			if utf8.RuneCountInString(g) > 64 {
				fields["group"] = "out_of_range"
			} else if g == "" {
				out.Group = nil
			} else {
				out.Group = &g
			}
		}
	}
	if in.Tags != nil {
		if len(*in.Tags) > 20 {
			fields["tags"] = "out_of_range"
		} else {
			seen := map[string]bool{}
			clean := make([]string, 0, len(*in.Tags))
			for _, raw := range *in.Tags {
				if !utf8.ValidString(raw) || strings.ContainsAny(raw, "\r\n") {
					fields["tags"] = "invalid_format"
					break
				}
				tag := strings.TrimSpace(raw)
				n := utf8.RuneCountInString(tag)
				if n == 0 || n > 32 {
					fields["tags"] = "invalid_format"
					break
				}
				if !seen[tag] {
					seen[tag] = true
					clean = append(clean, tag)
				}
			}
			if _, bad := fields["tags"]; !bad {
				out.Tags = clean
			}
		}
	}
	return out, fields
}
