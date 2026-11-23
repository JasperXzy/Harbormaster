package cli

import (
	"log/slog"
	"time"

	"github.com/JasperXzy/harbormaster/internal/cloud/aliyun"
	"github.com/JasperXzy/harbormaster/internal/collect"
	"github.com/JasperXzy/harbormaster/internal/config"
	"github.com/JasperXzy/harbormaster/internal/power"
	"github.com/JasperXzy/harbormaster/internal/store"
)

// Only serve calls this factory. run-once remains a read-only collection tool,
// even if the same configuration enables manual power in the serving process.
func newPowerEngine(c config.Config, s *store.Store, collector *collect.Engine, log *slog.Logger) *power.Engine {
	if c.Dev || !c.InstancePowerEnabled || collector == nil || collector.Box == nil {
		return nil
	}
	return &power.Engine{ProtectionPolicy: protectionPolicy(c), Store: s, Box: collector.Box, Writer: aliyun.NewPowerClient(nil, 200*time.Millisecond), Observer: aliyun.PowerObserver{Caller: aliyun.NewClient(nil, 200*time.Millisecond)}, Enabled: true, Workers: c.WorkerCount, Log: log}
}
