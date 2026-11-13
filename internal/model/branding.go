package model

import "time"

const DefaultBrandingName = "Harbormaster"

type Branding struct {
	Revision  int64         `json:"revision"`
	Name      string        `json:"name"`
	Logo      *BrandingLogo `json:"logo"`
	UpdatedAt *time.Time    `json:"updated_at"`
}

type BrandingLogo struct {
	URL       string `json:"url"`
	MediaType string `json:"media_type"`
	Width     int    `json:"width"`
	Height    int    `json:"height"`
	Bytes     int64  `json:"bytes"`
}
