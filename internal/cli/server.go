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
	"time"

	"github.com/JasperXzy/harbormaster/internal/api"
	"github.com/JasperXzy/harbormaster/internal/buildinfo"
	"github.com/JasperXzy/harbormaster/internal/config"
	"github.com/JasperXzy/harbormaster/internal/logging"
	"github.com/JasperXzy/harbormaster/internal/m0"
	"github.com/JasperXzy/harbormaster/internal/security"
	"github.com/JasperXzy/harbormaster/internal/store"
	"github.com/JasperXzy/harbormaster/internal/ui"
)

// Server returns a process exit code. Error output is deliberately restricted
// to fixed local messages; SDK or transport errors may contain secrets.
func Server(ctx context.Context, args []string, in io.Reader, out, errOut io.Writer) int {
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
		_, _ = io.WriteString(out, "hm-server <serve|run-once|migrate|version|healthcheck|reset-password> [--config path] [--dev] [--data-dir path] [--listen IP:port] [--frontend api|embedded]\nhm-server m0 <import|set-regions|plan|query> --account NAME (m0 --help for details)\nreset-password reads the new password from stdin; run-once performs only local M0 checks.\n")
		return 0
	}
	switch command {
	case "serve", "run-once", "migrate", "version", "healthcheck", "reset-password":
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
	if !c.Dev {
		if _, err = security.LoadBox(c.DataDir); err != nil {
			return fail(err.Error())
		}
	}
	s, err := store.Open(ctx, c.DataDir, c.Dev)
	if err != nil {
		return fail("database_initialization_failed")
	}
	defer s.Close()
	if c.Dev {
		if err = s.SeedDemo(ctx); err != nil {
			return fail("demo_seed_failed")
		}
	}
	switch command {
	case "migrate":
		log.Info("migrations_applied")
		return 0
	case "run-once":
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
	return serve(ctx, c, s, log)
}

func serve(ctx context.Context, c config.Config, s *store.Store, log *slog.Logger) int {
	listener, err := net.Listen("tcp", c.Listen)
	if err != nil {
		log.Error("listen_failed")
		return 1
	}
	var frontend http.Handler
	if c.Frontend == "embedded" {
		frontend = ui.Handler()
	}
	h := &http.Server{Handler: api.NewHandler(s, c.Dev, c.Timezone, frontend), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10}
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
