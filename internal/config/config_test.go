package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestConfiguration(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.json")
	_ = os.WriteFile(p, []byte(`{"listen":"127.0.0.1:9000","dev":false}`), 0600)
	t.Setenv("HM_LISTEN", "127.0.0.1:8085")
	t.Setenv("HM_DEV", "true")
	c, err := Load(p)
	if err != nil || c.Listen != "127.0.0.1:8085" || !c.Dev {
		t.Fatalf("precedence: %v", err)
	}
	t.Setenv("HM_LISTEN", "0.0.0.0:8085")
	if _, err = Load(p); err == nil {
		t.Fatal("public demo listener accepted")
	}
	_ = os.WriteFile(p, []byte(`{"master_key":"do-not-accept-secrets-in-config"}`), 0600)
	if _, err = Load(p); err == nil {
		t.Fatal("unknown credential setting accepted")
	}
}

func TestOfflineIPDatabaseDefaultsAndOverrides(t *testing.T) {
	c, err := Load("")
	if err != nil || !c.IPDBAutoUpdate || c.IPDBCityEnabled {
		t.Fatal(c, err)
	}
	t.Setenv("HM_IPDB_CITY_ENABLED", "true")
	t.Setenv("HM_IPDB_AUTO_UPDATE", "false")
	c, err = Load("")
	if err != nil || c.IPDBAutoUpdate || !c.IPDBCityEnabled {
		t.Fatal(c, err)
	}
	t.Setenv("HM_IPDB_CITY_ENABLED", "invalid")
	if _, err = Load(""); err == nil {
		t.Fatal("invalid city configuration accepted")
	}
}

func TestProductionAccessConfiguration(t *testing.T) {
	t.Setenv("HM_PUBLIC_URL", "https://panel.example")
	t.Setenv("HM_TRUSTED_PROXIES", "127.0.0.1/32, ::1/128")
	t.Setenv("HM_ADMIN_ALLOW_CIDRS", "203.0.113.0/24")
	t.Setenv("HM_SESSION_TTL_SECONDS", "3600")
	c, err := Load("")
	if err != nil || c.SessionTTLSeconds != 3600 || len(c.TrustedProxies) != 2 || len(c.AdminAllowCIDRs) != 1 {
		t.Fatal("production overrides", err)
	}
	for _, v := range []string{"http://panel.example", "https://user:password@panel.example", "https://panel.example/path", "https://panel.example?key=value", "https://panel.example#fragment"} {
		t.Setenv("HM_PUBLIC_URL", v)
		if _, err = Load(""); err == nil {
			t.Fatal("invalid public origin accepted")
		}
	}
	t.Setenv("HM_PUBLIC_URL", "https://panel.example")
	t.Setenv("HM_TRUSTED_PROXIES", "not-a-network")
	if _, err = Load(""); err == nil {
		t.Fatal("invalid proxy network accepted")
	}
	t.Setenv("HM_TRUSTED_PROXIES", "")
	t.Setenv("HM_SESSION_TTL_SECONDS", "0")
	if _, err = Load(""); err == nil {
		t.Fatal("unbounded session accepted")
	}
}

func TestInstancePowerRequiresExplicitProductionEnable(t *testing.T) {
	t.Setenv("HM_INSTANCE_POWER_ENABLED", "false")
	c, err := Load("")
	if err != nil || c.InstancePowerEnabled {
		t.Fatal(c, err)
	}
	p := filepath.Join(t.TempDir(), "config.json")
	if err = os.WriteFile(p, []byte(`{"instance_power_enabled":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	c, err = Load(p)
	if err != nil || c.InstancePowerEnabled {
		t.Fatal("environment override failed", err)
	}
	t.Setenv("HM_INSTANCE_POWER_ENABLED", "true")
	c, err = Load("")
	if err != nil || !c.InstancePowerEnabled {
		t.Fatal(err)
	}
	t.Setenv("HM_DEV", "true")
	if _, err = Load(""); err == nil {
		t.Fatal("power enabled in development mode")
	}
	t.Setenv("HM_DEV", "false")
	t.Setenv("HM_INSTANCE_POWER_ENABLED", "sometimes")
	if _, err = Load(""); err == nil {
		t.Fatal("invalid power flag accepted")
	}
}

func TestInstanceReleaseRequiresIndependentProductionEnable(t *testing.T) {
	t.Setenv("HM_DEV", "false")
	t.Setenv("HM_INSTANCE_POWER_ENABLED", "true")
	t.Setenv("HM_INSTANCE_RELEASE_ENABLED", "false")
	c, err := Load("")
	if err != nil || c.InstanceReleaseEnabled || !c.InstancePowerEnabled {
		t.Fatal(c, err)
	}
	p := filepath.Join(t.TempDir(), "release.json")
	if err := os.WriteFile(p, []byte(`{"instance_release_enabled":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	if c, err := Load(p); err != nil || c.InstanceReleaseEnabled {
		t.Fatal("environment did not override release file flag", c, err)
	}
	t.Setenv("HM_INSTANCE_POWER_ENABLED", "false")
	t.Setenv("HM_INSTANCE_RELEASE_ENABLED", "true")
	c, err = Load("")
	if err != nil || !c.InstanceReleaseEnabled || c.InstancePowerEnabled {
		t.Fatal(c, err)
	}
	t.Setenv("HM_DEV", "true")
	if _, err := Load(""); err == nil {
		t.Fatal("development enabled release")
	}
	t.Setenv("HM_DEV", "false")
	t.Setenv("HM_INSTANCE_RELEASE_ENABLED", "sometimes")
	if _, err := Load(""); err == nil {
		t.Fatal("invalid release flag accepted")
	}
}

func TestSystemEventPollingConfiguration(t *testing.T) {
	c, err := Load("")
	if err != nil || c.SystemEventPollIntervalSeconds != 60 {
		t.Fatal(c, err)
	}
	t.Setenv("HM_SYSTEM_EVENT_POLL_INTERVAL_SECONDS", "30")
	c, err = Load("")
	if err != nil || c.SystemEventPollIntervalSeconds != 30 {
		t.Fatal(c, err)
	}
	for _, raw := range []string{"0", "14", "3601", "invalid"} {
		t.Setenv("HM_SYSTEM_EVENT_POLL_INTERVAL_SECONDS", raw)
		if _, err = Load(""); err == nil {
			t.Fatal("invalid interval accepted", raw)
		}
	}
}

func TestProbeHistoryRetentionDefaultsAndOverrides(t *testing.T) {
	c, err := Load("")
	if err != nil || c.ProbeHistoryRetentionDays != 30 {
		t.Fatal(c, err)
	}
	path := filepath.Join(t.TempDir(), "config.json")
	if err = os.WriteFile(path, []byte(`{"probe_history_retention_days":14}`), 0600); err != nil {
		t.Fatal(err)
	}
	c, err = Load(path)
	if err != nil || c.ProbeHistoryRetentionDays != 14 {
		t.Fatal(c, err)
	}
	t.Setenv("HM_PROBE_HISTORY_RETENTION_DAYS", "7")
	c, err = Load(path)
	if err != nil || c.ProbeHistoryRetentionDays != 7 {
		t.Fatal(c, err)
	}
	for _, raw := range []string{"0", "-1", "3651", "invalid"} {
		t.Setenv("HM_PROBE_HISTORY_RETENTION_DAYS", raw)
		if _, err = Load(""); err == nil {
			t.Fatal("invalid retention accepted", raw)
		}
	}
}
