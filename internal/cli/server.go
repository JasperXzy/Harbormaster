package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/JasperXzy/harbormaster/internal/api"
	"github.com/JasperXzy/harbormaster/internal/backup"
	"github.com/JasperXzy/harbormaster/internal/buildinfo"
	"github.com/JasperXzy/harbormaster/internal/cloud/aliyun"
	"github.com/JasperXzy/harbormaster/internal/collect"
	"github.com/JasperXzy/harbormaster/internal/config"
	"github.com/JasperXzy/harbormaster/internal/devmode"
	"github.com/JasperXzy/harbormaster/internal/logging"
	"github.com/JasperXzy/harbormaster/internal/m0"
	"github.com/JasperXzy/harbormaster/internal/model"
	"github.com/JasperXzy/harbormaster/internal/notify"
	"github.com/JasperXzy/harbormaster/internal/security"
	"github.com/JasperXzy/harbormaster/internal/store"
	"github.com/JasperXzy/harbormaster/internal/ui"
)

// Server returns a process exit code. Error output is deliberately restricted
// to fixed local messages; SDK or transport errors may contain secrets.
func Server(ctx context.Context, args []string, in io.Reader, out, errOut io.Writer) int {
	ctx = model.WithJobTrigger(ctx, "cli")
	log := logging.New(errOut, "info")
	fail := func(code string) int { log.Error(code); return 1 }
	if len(args) == 0 {
		args = []string{"serve"}
	}
	command := args[0]
	if command == "m0" {
		return m0.Run(ctx, args[1:], in, out, errOut)
	}
	if command == "help" || command == "--help" || command == "-h" {
		_, _ = io.WriteString(out, "hm-server <serve|run-once|migrate|version|healthcheck|reset-password|import-accounts|backup|restore> [--config path] [--dev] [--data-dir path] [--listen IP:port] [--frontend api|embedded]\nhm-server m0 <import|set-regions|plan|query> --account NAME (m0 --help for details)\nimport-accounts reads the encrypted M0 vault (--source-vault / --source-key-file); no cloud queries.\nreset-password reads the new password from stdin; run-once processes due read-only jobs in production; --dev performs local checks.\nbackup --output NEW_FILE reads the backup password from stdin (hidden on a terminal); requires an existing production database and its external master key.\nrestore --input BACKUP_FILE --data-dir NEW_DIRECTORY reads the backup password from stdin and uses the target external master key; cancels unfinished work.\n")
		return 0
	}
	switch command {
	case "serve", "run-once", "migrate", "version", "healthcheck", "reset-password", "import-accounts", "backup", "restore":
	default:
		return fail("unknown_command")
	}
	f := flag.NewFlagSet(command, flag.ContinueOnError)
	f.SetOutput(io.Discard)
	configFile := f.String("config", os.Getenv("HM_CONFIG"), "JSON configuration path")
	dev := f.Bool("dev", false, "use synthetic data")
	dir := f.String("data-dir", "", "SQLite directory")
	listen := f.String("listen", "", "listener")
	frontend := f.String("frontend", "", "api or embedded")
	var sourceVault, sourceKey string
	var backupOutput string
	var restoreInput string
	if command == "restore" {
		f.StringVar(&restoreInput, "input", "", "encrypted backup file")
	}
	if command == "backup" {
		f.StringVar(&backupOutput, "output", "", "new encrypted backup file")
	}
	if command == "import-accounts" {
		f.StringVar(&sourceVault, "source-vault", "", "encrypted M0 vault")
		f.StringVar(&sourceKey, "source-key-file", "", "M0 vault key file")
	}
	if err := f.Parse(args[1:]); err != nil || f.NArg() != 0 {
		return fail("invalid_arguments")
	}
	if command == "version" {
		_ = json.NewEncoder(out).Encode(map[string]string{"program": "hm-server", "version": buildinfo.Version, "commit": buildinfo.Commit, "build_time": buildinfo.BuildTime})
		return 0
	}
	c, err := config.Load(*configFile)
	if err != nil {
		return fail(err.Error())
	}
	f.Visit(func(x *flag.Flag) {
		switch x.Name {
		case "dev":
			c.Dev = *dev
		case "data-dir":
			c.DataDir = *dir
		case "listen":
			c.Listen = *listen
		case "frontend":
			c.Frontend = *frontend
		}
	})
	if err = c.Validate(); err != nil {
		return fail(err.Error())
	}
	log = logging.New(errOut, c.LogLevel)
	if command == "restore" && (c.Dev || restoreInput == "") {
		return fail("restore_requires_production_target_and_input")
	}
	if command == "backup" {
		if c.Dev || backupOutput == "" {
			return fail("backup_requires_production_database_and_output")
		}
		info, err := os.Stat(filepath.Join(c.DataDir, "harbormaster.db"))
		if err != nil || !info.Mode().IsRegular() {
			return fail("backup_source_not_found")
		}
	}
	if command == "healthcheck" {
		host, port, _ := net.SplitHostPort(c.Listen)
		if net.ParseIP(host).IsUnspecified() {
			host = "127.0.0.1"
		}
		req, _ := http.NewRequestWithContext(ctx, "GET", "http://"+net.JoinHostPort(host, port)+"/readyz", nil)
		client := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		res, err := client.Do(req)
		if err != nil {
			return fail("healthcheck_failed")
		}
		defer res.Body.Close()
		if res.StatusCode != 200 {
			return fail("healthcheck_failed")
		}
		_, _ = io.WriteString(out, "ready\n")
		return 0
	}
	var box *security.Box
	if !c.Dev {
		if box, err = security.LoadBox(c.DataDir); err != nil {
			return fail(err.Error())
		}
	}
	if command == "restore" {
		summary, err := restoreBackup(ctx, box, in, out, restoreInput, c.DataDir)
		if err != nil {
			if errors.Is(err, backup.ErrPassword) || errors.Is(err, backup.ErrRestoreTargetExists) {
				return fail(err.Error())
			}
			return fail("backup_restore_failed")
		}
		log.Info("backup_restore_complete", "sessions_revoked", true, "cancelled_jobs", summary.CancelledJobs, "cancelled_notifications", summary.CancelledNotifications, "cloud_queries", 0, "cloud_writes", 0)
		return 0
	}
	s, err := store.Open(ctx, c.DataDir, c.Dev)
	if err != nil {
		return fail("database_initialization_failed")
	}
	defer s.Close()
	if !c.Dev {
		if err = s.BindMasterKey(ctx, box); err != nil {
			return fail("master_key_verification_failed")
		}
	}
	if c.Dev {
		if err = s.SeedDemo(ctx); err != nil {
			return fail("demo_seed_failed")
		}
	}
	var engine *collect.Engine
	if !c.Dev {
		engine = &collect.Engine{Store: s, Box: box, Scanner: aliyun.Inventory{Caller: aliyun.NewClient(nil, 200*time.Millisecond)}, Workers: c.WorkerCount, Interval: time.Duration(c.ResourceScanIntervalSeconds) * time.Second, CDTInterval: time.Duration(c.CDTPollIntervalSeconds) * time.Second, Log: log, Timezone: c.Timezone}
	}
	switch command {
	case "backup":
		if err := exportBackup(ctx, s, box, in, out, backupOutput); err != nil {
			if errors.Is(err, backup.ErrPassword) || errors.Is(err, backup.ErrOutputExists) {
				return fail(err.Error())
			}
			return fail("backup_failed")
		}
		log.Info("backup_complete", "cloud_queries", 0, "cloud_writes", 0)
		return 0
	case "migrate":
		log.Info("migrations_applied")
		return 0
	case "import-accounts":
		if engine == nil {
			return fail("import_requires_production_database")
		}
		imported, skipped := 0, 0
		err := m0.ImportAccounts(sourceVault, sourceKey, func(in model.AccountCreate) error {
			_, _, err := engine.CreateAccount(ctx, in)
			if errors.Is(err, store.ErrNameTaken) {
				skipped++
				return nil
			}
			if err == nil {
				imported++
			}
			return err
		})
		if err != nil {
			return fail("account_import_failed")
		}
		log.Info("account_import_complete", "imported", imported, "skipped_existing_names", skipped, "cloud_queries", 0, "cloud_writes", 0)
		return 0
	case "run-once":
		if engine != nil {
			if err = engine.Schedule(ctx); err != nil {
				return fail("schedule_failed")
			}
			for {
				ran, err := engine.RunOne(ctx)
				if err != nil {
					return fail("worker_step_failed")
				}
				if !ran {
					break
				}
			}
			if err = s.EvaluateAlerts(ctx, time.Now().UTC()); err != nil {
				return fail("alert_evaluation_failed")
			}
			log.Info("run_once_complete", "cloud_writes", 0)
			return 0
		}
		if err = s.Ready(ctx); err != nil {
			return fail("local_check_failed")
		}
		log.Info("run_once_complete", "cloud_queries", 0, "cloud_writes", 0, "phase", "m0")
		return 0
	case "reset-password":
		password, err := io.ReadAll(io.LimitReader(in, 1026))
		if err != nil {
			return fail("password_input_failed")
		}
		defer clear(password)
		password = bytes.TrimSuffix(password, []byte("\n"))
		password = bytes.TrimSuffix(password, []byte("\r"))
		h, err := security.HashPassword(password)
		if err != nil {
			return fail("password_must_be_12_to_1024_bytes")
		}
		if err = s.ResetPassword(ctx, h); err != nil {
			if err.Error() == "administrator_not_initialized" {
				return fail("administrator_not_initialized")
			}
			return fail("password_reset_failed")
		}
		log.Info("password_reset_complete", "sessions_revoked", true)
		return 0
	}
	if !c.Dev {
		if c.PublicURL == "" {
			return fail("public_url_required_for_production_serve")
		}
		initialized, err := s.Initialized(ctx)
		if err != nil {
			return fail("setup_state_unavailable")
		}
		if !initialized {
			token, err := security.LoadSetupToken(c.DataDir)
			if err != nil {
				return fail(err.Error())
			}
			defer clear(token)
			if err = s.PrepareSetup(ctx, token, time.Now()); err != nil {
				return fail("setup_preparation_failed")
			}
			log.Info("setup_required", "credential_source", "external_environment_or_file")
		}
	}
	return serve(ctx, c, s, engine, log)
}

func serve(ctx context.Context, c config.Config, s *store.Store, engine *collect.Engine, log *slog.Logger) int {
	listener, err := net.Listen("tcp", c.Listen)
	if err != nil {
		log.Error("listen_failed")
		return 1
	}
	var frontend http.Handler
	if c.Frontend == "embedded" {
		frontend = ui.Handler()
	}
	var panel http.Handler
	if c.Dev {
		panel = devmode.New(s, c.Timezone)
	} else {
		panel = api.NewProductionPanel(s, c, engine)
	}
	if engine != nil {
		workerCtx, cancel := context.WithCancel(ctx)
		var workers sync.WaitGroup
		workers.Add(2)
		go func() { defer workers.Done(); engine.Run(workerCtx) }()
		go func() { defer workers.Done(); (notify.Evaluator{Store: s, Log: log}).Run(workerCtx) }()
		defer func() { cancel(); workers.Wait() }()
	}
	h := &http.Server{Handler: api.NewHandler(s, panel, frontend), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10, BaseContext: func(net.Listener) context.Context { return ctx }}
	done := make(chan error, 1)
	go func() { done <- h.Serve(listener) }()
	log.Info("server_started", "listen", listener.Addr().String(), "development", c.Dev, "frontend", c.Frontend, "cloud_writes", false)
	select {
	case err = <-done:
		if !errors.Is(err, http.ErrServerClosed) {
			log.Error("server_failed")
			return 1
		}
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if h.Shutdown(shutdown) != nil {
			_ = h.Close()
			log.Error("shutdown_timeout")
			return 1
		}
	}
	log.Info("server_stopped")
	return 0
}
