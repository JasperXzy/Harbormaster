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
