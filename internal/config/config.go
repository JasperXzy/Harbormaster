// Package config loads non-secret settings. Secrets are deliberately not part of Config.
package config

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"strconv"
	"time"
	_ "time/tzdata"
)

type Config struct {
	Listen   string `json:"listen"`
	DataDir  string `json:"data_dir"`
	Dev      bool   `json:"dev"`
	LogLevel string `json:"log_level"`
	Timezone string `json:"timezone"`
	Frontend string `json:"frontend"`
}

// Load applies defaults, an optional JSON file, then environment overrides.
func Load(path string) (Config, error) {
	c := Config{Listen: "127.0.0.1:8080", DataDir: "data", LogLevel: "info", Timezone: "Asia/Shanghai", Frontend: "api"}
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
	for k, p := range map[string]*string{"HM_LISTEN": &c.Listen, "HM_DATA_DIR": &c.DataDir, "HM_LOG_LEVEL": &c.LogLevel, "HM_TIMEZONE": &c.Timezone, "HM_FRONTEND": &c.Frontend} {
		if v, ok := os.LookupEnv(k); ok {
			*p = v
		}
	}
	if v, ok := os.LookupEnv("HM_DEV"); ok {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return c, errors.New("invalid HM_DEV")
		}
		c.Dev = b
	}
	return c, c.Validate()
}

func (c Config) Validate() error {
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
