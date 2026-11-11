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
