package model

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/JasperXzy/harbormaster/internal/ipinfo"
)

type TracePTR struct {
	Status    string   `json:"status"`
	Names     []string `json:"names"`
	Truncated bool     `json:"truncated"`
}
type TraceAddressInfo struct {
	ipinfo.Record
	PTR TracePTR `json:"ptr"`
}
type TraceHopInfo struct {
	TTL               int      `json:"ttl"`
	Attempts          int      `json:"attempts"`
	Responses         int      `json:"responses"`
	NoResponsePercent *float64 `json:"no_response_percent"`
	MinimumMS         *float64 `json:"minimum_ms"`
	MaximumMS         *float64 `json:"maximum_ms"`
	AverageMS         *float64 `json:"average_ms"`
	Addresses         []string `json:"addresses"`
	ASNCandidates     []uint32 `json:"asn_candidates"`
	UnknownASN        bool     `json:"unknown_asn"`
	CountryCodes      []string `json:"country_codes"`
	UnknownLocation   bool     `json:"unknown_location"`
}
type TraceNetworkLabel struct {
	Code         string   `json:"code"`
	ASN          uint32   `json:"asn"`
	Addresses    []string `json:"addresses"`
	TTLs         []int    `json:"ttls"`
	ServiceTier  *string  `json:"service_tier"`
	CrossBorder  *string  `json:"cross_border"`
	ReferenceURL string   `json:"reference_url"`
}
type TraceEnrichment struct {
	EnrichedAt       time.Time            `json:"enriched_at"`
	Status           string               `json:"status"`
	ASNDatabase      *ipinfo.Dataset      `json:"asn_database"`
	LocationDatabase *ipinfo.Dataset      `json:"location_database"`
	Addresses        []TraceAddressInfo   `json:"addresses"`
	Hops             []TraceHopInfo       `json:"hops"`
	Labels           []TraceNetworkLabel  `json:"labels"`
	DetourStatus     string               `json:"detour_status"`
	Attribution      []ipinfo.Attribution `json:"attribution"`
}

func traceEnrichmentText(b *strings.Builder, e *TraceEnrichment) {
	if e == nil {
		return
	}
	fmt.Fprintf(b, "enriched_at: %s\ndetour_status: %s\n", e.EnrichedAt.Format(time.RFC3339Nano), e.DetourStatus)
	// JSON lines preserve nullable values and dataset evidence in a text export.
	write := func(kind string, value any) {
		raw, _ := json.Marshal(value)
		fmt.Fprintf(b, "%s: %s\n", kind, raw)
	}
	write("asn_database", e.ASNDatabase)
	write("location_database", e.LocationDatabase)
	for _, v := range e.Addresses {
		write("ip_info", v)
	}
	for _, v := range e.Hops {
		write("hop_info", v)
	}
	for _, v := range e.Labels {
		write("network_label", v)
	}
	for _, v := range e.Attribution {
		fmt.Fprintf(b, "attribution: %s <%s> (%s, %s)\n", v.Name, v.URL, v.License, v.LicenseURL)
	}
	b.WriteString("PTR is an unverified DNS name. IP ASN candidates are not a measured BGP AS path.\nGeolocation is a database estimate; country/city sequences do not establish detour.\nno_response_percent describes unanswered attempts at that TTL, not forwarding packet loss.\n")
}
