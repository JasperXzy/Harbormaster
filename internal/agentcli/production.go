package agentcli

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"math/rand/v2"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/JasperXzy/harbormaster/internal/agentclient"
	"github.com/JasperXzy/harbormaster/internal/agentmeter"
	"github.com/JasperXzy/harbormaster/internal/agentstate"
	"github.com/JasperXzy/harbormaster/internal/buildinfo"
	"github.com/JasperXzy/harbormaster/internal/security"
)

func production(ctx context.Context, command, server, dir, interfaces string, codeStdin bool, interval time.Duration, log *slog.Logger) error {
	if runtime.GOOS != "linux" {
		return agentmeter.ErrUnsupported
	}
	if command != "register" && command != "serve" && command != "run-once" && command != "remove-counters" {
		return errors.New("unknown_command")
	}
	if codeStdin && command != "register" {
		return errors.New("registration_flag_requires_register")
	}
	box, err := security.LoadBox(dir)
	if err != nil {
		return err
	}
	journal, err := agentstate.Open(dir, box)
	if err != nil {
		return err
	}
	defer journal.Close()
	if command == "remove-counters" {
		return agentmeter.Remove(strings.Split(interfaces, ","))
	}
	client := &agentclient.Client{HTTP: agentclient.NewHTTP(), Journal: journal, Version: buildinfo.Version, ReportIntervalSeconds: int(interval / time.Second)}
	defer client.HTTP.CloseIdleConnections()
	if server != "" {
		origin, err := agentclient.Origin(server)
		if err != nil {
			return err
		}
		if saved := journal.Meta().Identity.Server; saved != "" && saved != origin {
			return errors.New("agent_server_origin_mismatch")
		}
	}
	if command == "register" {
		if codeStdin {
			raw, err := io.ReadAll(io.LimitReader(os.Stdin, 257))
			if err != nil || len(raw) > 256 {
				return errors.New("invalid_registration_input")
			}
			code := strings.TrimSpace(string(raw))
			clear(raw)
			metadataCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			metadata, err := agentclient.Metadata(metadataCtx, client.HTTP)
			cancel()
			if err != nil {
				return err
			}
			if err = client.PrepareRegistration(server, code, metadata); err != nil {
				return err
			}
		}
		if err = client.Register(ctx); err != nil {
			return err
		}
		log.Info("agent_registered")
		return nil
	}
	if err = client.Register(ctx); err != nil {
		return err
	}
	meter, err := agentmeter.Open(strings.Split(interfaces, ","))
	if err != nil {
		return err
	}
	sample := func() error {
		dropped, err := journal.PruneBefore(time.Now().Add(-7*24*time.Hour + 5*time.Minute))
		if err != nil {
			return err
		}
		if dropped > 0 {
			log.Warn("agent_expired_samples_discarded", "count", dropped)
		}
		value, err := meter.Read()
		if err != nil {
			return err
		}
		return client.Sample(value, time.Now())
	}
	if err = sample(); err != nil {
		return err
	}
	if command == "run-once" {
		_, err = client.Flush(ctx)
		return err
	}
	workerCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- delivery(workerCtx, client, log) }()
	// Join the delivery worker before closing the encrypted journal.
	defer func() { cancel(); <-result }()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	log.Info("agent_started", "report_interval_seconds", int(interval/time.Second), "rules_version", agentmeter.RulesVersion)
	for {
		select {
		case <-ctx.Done():
			return nil
		case err = <-result:
			result <- err
			return err
		case <-ticker.C:
			if err = sample(); err != nil {
				return err
			}
		}
	}
}
func delivery(ctx context.Context, client *agentclient.Client, log *slog.Logger) error {
	failures := 0
	for {
		sent, err := client.Flush(ctx)
		if ctx.Err() != nil {
			return nil
		}
		delay := time.Second
		if err != nil {
			if !errors.Is(err, agentclient.ErrTransport) {
				return err
			}
			failures++
			shift := min(failures, 6)
			delay = time.Duration(1<<shift) * time.Second
			delay = min(delay, 60*time.Second)/2 + time.Duration(rand.Int64N(int64(min(delay, 60*time.Second)/2)))
			if failures == 1 || failures%10 == 0 {
				log.Warn("agent_delivery_unavailable", "attempts", failures)
			}
		} else {
			failures = 0
			if sent {
				delay = 100 * time.Millisecond
			}
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
}
