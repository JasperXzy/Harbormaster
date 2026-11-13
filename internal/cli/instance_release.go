package cli

import (
	"log/slog"
	"time"

	"github.com/JasperXzy/harbormaster/internal/cloud/aliyun"
	"github.com/JasperXzy/harbormaster/internal/collect"
	"github.com/JasperXzy/harbormaster/internal/config"
	"github.com/JasperXzy/harbormaster/internal/release"
	"github.com/JasperXzy/harbormaster/internal/store"
)

// Only serve calls this factory. The release capability is independently
// disabled by default and cannot be started in demo or read-only run-once.
func newReleaseEngine(c config.Config, s *store.Store, collector *collect.Engine, log *slog.Logger) *release.Engine {
	if c.Dev || !c.InstanceReleaseEnabled || collector == nil || collector.Box == nil {
		return nil
	}
	return &release.Engine{Store: s, Box: collector.Box, Writer: aliyun.NewReleaseClient(nil, 200*time.Millisecond), Observer: aliyun.ReleaseObserver{Caller: aliyun.NewClient(nil, 200*time.Millisecond)}, Enabled: true, Workers: c.WorkerCount, Log: log}
}
