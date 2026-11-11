package agentcli

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/JasperXzy/harbormaster/internal/agentproto"
	"github.com/JasperXzy/harbormaster/internal/buildinfo"
	"github.com/JasperXzy/harbormaster/internal/logging"
)

// Agent separates loopback fixtures from the production Linux collector.
func Agent(ctx context.Context, args []string, out, errOut io.Writer) int {
	log := logging.New(errOut, "info")
	if len(args) == 0 {
		args = []string{"serve"}
	}
	cmd := args[0]
	if cmd == "help" || cmd == "--help" || cmd == "-h" {
		_, _ = io.WriteString(out, "hm-agent <register|serve|run-once|remove-counters|version> [--server https://panel.example] [--data-dir /var/lib/harbormaster-agent] [--interfaces eth0]\nregister --registration-code-stdin reads a one-time code from stdin; without the flag it retries the encrypted pending registration. Production requires Linux and HM_MASTER_KEY or HM_MASTER_KEY_FILE outside data-dir.\n--dev emits synthetic counters every 10 seconds; optional --server must be loopback HTTP.\n")
		return 0
	}
	f := flag.NewFlagSet(cmd, flag.ContinueOnError)
	f.SetOutput(io.Discard)
	dev := f.Bool("dev", false, "synthetic counters only")
	server := f.String("server", "", "server origin")
	dataDir := f.String("data-dir", "/var/lib/harbormaster-agent", "encrypted agent state directory")
	interfaces := f.String("interfaces", "", "explicit public interfaces separated by commas")
	codeStdin := f.Bool("registration-code-stdin", false, "read one-time code from stdin")
	if f.Parse(args[1:]) != nil || f.NArg() != 0 {
		log.Error("invalid_arguments")
		return 1
	}
	if cmd == "version" {
		_ = json.NewEncoder(out).Encode(map[string]string{"program": "hm-agent", "version": buildinfo.Version})
		return 0
	}
	if !*dev {
		if err := production(ctx, cmd, *server, *dataDir, *interfaces, *codeStdin, log); err != nil {
			log.Error("agent_stopped", "reason", err.Error())
			return 1
		}
		return 0
	}
	if cmd != "serve" && cmd != "run-once" {
		log.Error("unknown_command")
		return 1
	}
	if *server != "" {
		u, err := url.Parse(*server)
		if err != nil || u.Scheme != "http" || u.User != nil || !net.ParseIP(u.Hostname()).IsLoopback() || u.RawQuery != "" || u.Fragment != "" || u.Path != "" {
			log.Error("demo_server_must_be_loopback_http_origin")
			return 1
		}
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		log.Error("random_source_failed")
		return 1
	}
	epoch := hex.EncodeToString(nonce[:])
	start := time.Now()
	var seq int64
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer client.CloseIdleConnections()
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		seq++
		r := agentproto.Report{Version: 1, AgentID: agentproto.DemoAgentID, CounterEpoch: epoch, Sequence: seq, SampledAt: time.Now().UTC(), UptimeMS: time.Since(start).Milliseconds(), RXBytes: (seq - 1) * 1000000, TXBytes: (seq - 1) * 800000, AgentVersion: buildinfo.Version}
		if *server == "" {
			if json.NewEncoder(out).Encode(r) != nil {
				return 1
			}
		} else {
			b, _ := json.Marshal(r)
			req, _ := http.NewRequestWithContext(ctx, "POST", *server+agentproto.Path, bytes.NewReader(b))
			req.Header.Set("Authorization", "Bearer "+agentproto.DemoToken)
			req.Header.Set("Content-Type", "application/json")
			res, err := client.Do(req)
			ok := false
			if err == nil {
				ok = res.StatusCode == 200
				_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 16<<10))
				_ = res.Body.Close()
			}
			if !ok {
				log.Warn("demo_report_failed")
				if cmd == "run-once" {
					return 1
				}
			} else {
				log.Info("demo_report_accepted", "sequence", seq)
			}
		}
		if cmd == "run-once" {
			return 0
		}
		select {
		case <-ctx.Done():
			return 0
		case <-ticker.C:
		}
	}
}
