// Package config loads non-secret settings. Secrets are deliberately not part of Config.
package config

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata"
)

type Config struct {
	Listen                      string   `json:"listen"`
	DataDir                     string   `json:"data_dir"`
	Dev                         bool     `json:"dev"`
	LogLevel                    string   `json:"log_level"`
	Timezone                    string   `json:"timezone"`
	Frontend                    string   `json:"frontend"`
	PublicURL                   string   `json:"public_url"`
	TrustedProxies              []string `json:"trusted_proxies"`
	AdminAllowCIDRs             []string `json:"admin_allow_cidrs"`
	SessionTTLSeconds           int      `json:"session_ttl_seconds"`
	WorkerCount                 int      `json:"worker_count"`
	ResourceScanIntervalSeconds int      `json:"resource_scan_interval_seconds"`
}

// Load applies defaults, an optional JSON file, then environment overrides.
func Load(path string) (Config, error) {
	c := Config{Listen: "127.0.0.1:8080", DataDir: "data", LogLevel: "info", Timezone: "Asia/Shanghai", Frontend: "api", SessionTTLSeconds: 43200, WorkerCount: 2, ResourceScanIntervalSeconds: 300}
	if path != "" {
		f, err := os.Open(path)
		if err != nil {
			return c, errors.New("cannot open configuration file")
		}
		defer f.Close()
		d := json.NewDecoder(io.LimitReader(f, 64<<10))
		d.DisallowUnknownFields()
		if err := d.Decode(&c); err != nil {
			return c, errors.New("invalid configuration JSON")
		}
		if d.Decode(new(any)) != io.EOF {
			return c, errors.New("configuration must contain one JSON object")
		}
	}
	for k, p := range map[string]*string{"HM_LISTEN": &c.Listen, "HM_DATA_DIR": &c.DataDir, "HM_LOG_LEVEL": &c.LogLevel, "HM_TIMEZONE": &c.Timezone, "HM_FRONTEND": &c.Frontend, "HM_PUBLIC_URL": &c.PublicURL} {
		if v, ok := os.LookupEnv(k); ok {
			*p = v
		}
	}
	for k, p := range map[string]*[]string{"HM_TRUSTED_PROXIES": &c.TrustedProxies, "HM_ADMIN_ALLOW_CIDRS": &c.AdminAllowCIDRs} {
		if v, ok := os.LookupEnv(k); ok {
			*p = nil
			if strings.TrimSpace(v) != "" {
				for _, item := range strings.Split(v, ",") {
					*p = append(*p, strings.TrimSpace(item))
				}
			}
		}
	}
	if v, ok := os.LookupEnv("HM_SESSION_TTL_SECONDS"); ok {
		n, err := strconv.Atoi(v)
		if err != nil {
			return c, errors.New("invalid HM_SESSION_TTL_SECONDS")
		}
		c.SessionTTLSeconds = n
	}
	if v, ok := os.LookupEnv("HM_DEV"); ok {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return c, errors.New("invalid HM_DEV")
		}
		c.Dev = b
	}
	for k, p := range map[string]*int{"HM_WORKER_COUNT": &c.WorkerCount, "HM_RESOURCE_SCAN_INTERVAL_SECONDS": &c.ResourceScanIntervalSeconds} {
		if v, ok := os.LookupEnv(k); ok {
			n, err := strconv.Atoi(v)
			if err != nil {
				return c, errors.New("invalid collection configuration")
			}
			*p = n
		}
	}
	return c, c.Validate()
}

func (c Config) Validate() error {
	if c.WorkerCount < 1 || c.WorkerCount > 8 || c.ResourceScanIntervalSeconds < 60 || c.ResourceScanIntervalSeconds > 86400 {
		return errors.New("invalid worker count or resource scan interval")
	}
	if c.SessionTTLSeconds < 300 || c.SessionTTLSeconds > 604800 {
		return errors.New("session_ttl_seconds must be between 300 and 604800")
	}
	if c.PublicURL != "" {
		u, err := url.Parse(c.PublicURL)
		if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
			return errors.New("public_url must be an HTTPS origin")
		}
	}
	for _, list := range [][]string{c.TrustedProxies, c.AdminAllowCIDRs} {
		for _, value := range list {
			if _, err := netip.ParsePrefix(value); err != nil {
				return errors.New("invalid proxy or administrator CIDR")
			}
		}
	}
	if c.Frontend != "api" && c.Frontend != "embedded" {
		return errors.New("frontend must be api or embedded")
	}
	host, port, err := net.SplitHostPort(c.Listen)
	if err != nil {
		return errors.New("listen must be IP:port")
	}
	ip := net.ParseIP(host)
	p, err := strconv.Atoi(port)
	if ip == nil || err != nil || p < 1 || p > 65535 {
		return errors.New("invalid listen address")
	}
	if c.Dev && !ip.IsLoopback() {
		return errors.New("development mode requires a loopback listener")
	}
	if c.DataDir == "" {
		return errors.New("data_dir is required")
	}
	if _, err := time.LoadLocation(c.Timezone); err != nil {
		return errors.New("invalid timezone")
	}
	switch c.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return errors.New("invalid log_level")
	}
	return nil
}
