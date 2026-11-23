package model

import "time"

const NetworkRateFreshness = 10 * time.Minute

type CloudNetworkRate struct {
	Samples       []CloudRatePoint `json:"-"`
	MetricName    string           `json:"metric_name"`
	Unit          string           `json:"unit"`
	PeriodSeconds int              `json:"period_seconds"`
	Value         *float64         `json:"value"`
	Status        string           `json:"status"`
	ObservedAt    *time.Time       `json:"observed_at"`
	CheckedAt     time.Time        `json:"checked_at"`
	LastError     *APIError        `json:"last_error"`
}
type CloudNetworkStream struct {
	IP      *string          `json:"ip"`
	Ingress CloudNetworkRate `json:"ingress"`
	Egress  CloudNetworkRate `json:"egress"`
}
type CloudNetwork struct {
	Source           string               `json:"source"`
	NetworkType      string               `json:"network_type"`
	TopologyComplete bool                 `json:"topology_complete"`
	CheckedAt        time.Time            `json:"checked_at"`
	Streams          []CloudNetworkStream `json:"streams"`
}

func EmptyNetworkRate(name string, now time.Time) CloudNetworkRate {
	return CloudNetworkRate{MetricName: name, Unit: "bit/s", PeriodSeconds: 60, Status: "unknown", CheckedAt: now, LastError: Error("metric_data_unavailable")}
}
func (m *CloudNetworkRate) Age(now time.Time) {
	if m.Status == "ok" && (m.ObservedAt == nil || m.ObservedAt.After(now) || m.ObservedAt.Add(NetworkRateFreshness).Before(now)) {
		m.Status = "stale"
	}
}
func NetworkMetricNames(kind string) (string, string) {
	switch kind {
	case "classic":
		return "InternetInRate", "InternetOutRate"
	case "vpc":
		return "VPC_PublicIP_InternetInRate", "VPC_PublicIP_InternetOutRate"
	case "swas":
		return "VPC_PUBLICIP_INTERNETIN_RATE", "VPC_PUBLICIP_INTERNETOUT_RATE"
	}
	return "", ""
}
