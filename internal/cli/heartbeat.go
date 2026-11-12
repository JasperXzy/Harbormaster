package cli

import (
	"log/slog"
	"net"
	"net/url"

	"github.com/JasperXzy/harbormaster/internal/collect"
	"github.com/JasperXzy/harbormaster/internal/config"
	"github.com/JasperXzy/harbormaster/internal/heartbeat"
	"github.com/JasperXzy/harbormaster/internal/store"
)

func newHeartbeatRunner(c config.Config, s *store.Store, e *collect.Engine, listener net.Listener, log *slog.Logger) *heartbeat.Runner {
	if c.Dev || e == nil || e.Box == nil {
		return nil
	}
	host, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		return nil
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsUnspecified() {
		if ip.To4() != nil {
			host = "127.0.0.1"
		} else {
			host = "::1"
		}
	}
	target := (&url.URL{Scheme: "http", Host: net.JoinHostPort(host, port), Path: "/readyz"}).String()
	gate := heartbeat.NewHealthGate(target, e.SchedulerProgress)
	return &heartbeat.Runner{Store: s, Box: e.Box, Sender: heartbeat.NewSender(), Healthy: gate.Healthy, Log: log}
}
