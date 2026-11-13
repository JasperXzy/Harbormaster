package m0

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/JasperXzy/harbormaster/internal/security"
	"golang.org/x/term"
)

const usage = `hm-server m0 <import|set-regions|plan|query> --account NAME [options]
  import: prompts for site, regions, AccessKey ID, Secret and optional STS token.
          Input is hidden; credentials are encrypted outside the repository.
          --replace explicitly replaces an existing account.
  plan:   prints the read-only inventory/billing/metrics plan without networking.
  query:  runs that plan, emits a sanitized JSON report; no cloud writes.
  set-regions: replaces only the local region list, preserving credentials.
Options:
  --vault PATH       encrypted credentials file (outside a Git repository)
  --key-file PATH    master-key file (outside the vault directory and repository)
  --check NAME       inventory (default), billing, instance-billing, metrics, metric-schema, ecs-metric-schema, ecs-credit-specs, swas-cpu, swas-network, event-schema, system-events or cdt
  --month YYYY-MM    required for billing
  --region ID        select one region already stored for this account
  --regions LIST     comma-separated list for set-regions
  --metric-source    ecs, ecs-network, eip or bandwidth (metrics only)
  --resource-id ID   cloud instance/EIP/shared-bandwidth ID (metrics, swas-cpu or swas-network)
  --output PATH     write query report to a NEW private file outside repositories
HM_MASTER_KEY / HM_MASTER_KEY_FILE override the default key source. Never pass
AccessKey values as flags. Import/plan make no cloud requests. Query rejects DEBUG.
`

func Run(ctx context.Context, args []string, in io.Reader, out, errOut io.Writer) int {
	fail := func(code string) int { _ = json.NewEncoder(errOut).Encode(map[string]string{"error": code}); return 1 }
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" {
		_, _ = io.WriteString(out, usage)
		return 0
	}
	command := args[0]
	if command != "import" && command != "plan" && command != "query" && command != "set-regions" {
		return fail("unknown_m0_command")
	}
	defaultVault, defaultKey, err := defaultPaths()
	if err != nil {
		return fail("home_directory_unavailable")
	}
	f := flag.NewFlagSet("m0", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	account := f.String("account", "", "account alias")
	vaultPath := f.String("vault", defaultVault, "encrypted vault")
	keyFile := f.String("key-file", defaultKey, "separate key file")
	replace := f.Bool("replace", false, "replace imported account")
	check := f.String("check", "inventory", "check group")
	month := f.String("month", "", "billing month")
	region := f.String("region", "", "selected region")
	regions := f.String("regions", "", "replacement region list")
	source := f.String("metric-source", "", "metric source")
	resource := f.String("resource-id", "", "metric resource")
	output := f.String("output", "", "report path")
	if f.Parse(args[1:]) != nil || f.NArg() != 0 {
		return fail("invalid_arguments")
	}
	if !namePattern.MatchString(*account) {
		return fail("account_name_required")
	}
	// Avoid silently ignoring an explicitly chosen key path when an environment
	// key source is active. Default-path fallback remains convenient and documented.
	keyFlag := false
	f.Visit(func(v *flag.Flag) {
		if v.Name == "key-file" {
			keyFlag = true
		}
	})
	if keyFlag && (os.Getenv("HM_MASTER_KEY") != "" || os.Getenv("HM_MASTER_KEY_FILE") != "") {
		return fail("ambiguous_master_key_sources")
	}
	if command == "set-regions" {
		list := strings.Split(*regions, ",")
		for i := range list {
			list[i] = strings.TrimSpace(list[i])
		}
		if err = setRegions(*vaultPath, *keyFile, *account, list); err != nil {
			return fail(err.Error())
		}
		_ = json.NewEncoder(out).Encode(map[string]any{"status": "regions_updated", "account": *account, "regions": list, "cloud_requests": 0})
		return 0
	}
	if command == "import" {
		file, ok := in.(*os.File)
		if !ok || !term.IsTerminal(int(file.Fd())) {
			return fail("interactive_terminal_required")
		}
		path, e := externalPath(*vaultPath)
		if e != nil {
			return fail(e.Error())
		}
		p := profile{Name: *account}
		// Disable OS echo before displaying any prompt. ReadPassword alone, when
		// called after a prompt, leaves a race where an immediate paste is echoed.
		state, e := term.MakeRaw(int(file.Fd()))
		if e != nil {
			return fail("terminal_unavailable")
		}
		defer term.Restore(int(file.Fd()), state)
		terminal := term.NewTerminal(terminalIO{file, errOut}, "")
		read := func(prompt string) (string, error) {
			b, e := hiddenInput(ctx, terminal, prompt+" (hidden): ")
			if e != nil {
				return "", e
			}
			defer clear(b)
			return strings.TrimSpace(string(b)), nil
		}
		if p.Site, err = read("Account site: china or international"); err != nil {
			return fail("input_cancelled")
		}
		regions, e := read("Regions, comma-separated")
		if e != nil {
			return fail("input_cancelled")
		}
		p.Regions = strings.Split(regions, ",")
		for i := range p.Regions {
			p.Regions[i] = strings.TrimSpace(p.Regions[i])
		}
		if p.AccessKeyID, err = read("Read-only RAM AccessKey ID"); err != nil {
			return fail("input_cancelled")
		}
		if p.AccessKeySecret, err = read("AccessKey Secret"); err != nil {
			return fail("input_cancelled")
		}
		if p.SecurityToken, err = read("STS token, Enter if not using STS"); err != nil {
			return fail("input_cancelled")
		}
		if err = saveProfile(path, *keyFile, p, *replace); err != nil {
			return fail(err.Error())
		}
		_ = json.NewEncoder(out).Encode(map[string]any{"status": "imported", "account": p.Name, "access_key_id_masked": security.MaskAccessKeyID(p.AccessKeyID), "vault": path, "cloud_requests": 0})
		return 0
	}
	if command == "query" && (sdkDebugAtStartup || os.Getenv("DEBUG") != "") {
		return fail("sdk_debug_must_be_disabled")
	}
	p, err := getProfile(*vaultPath, *keyFile, *account)
	if err != nil {
		return fail(err.Error())
	}
	if *region != "" {
		found := false
		for _, r := range p.Regions {
			if r == *region {
				found = true
			}
		}
		if !found {
			return fail("region_not_in_account_profile")
		}
		p.Regions = []string{*region}
	}
	ops, err := plan(p, queryOptions{*check, *month, *source, *resource})
	if err != nil {
		return fail(err.Error())
	}
	if command == "plan" {
		_ = json.NewEncoder(out).Encode(map[string]any{"account": p.Name, "access_key_id_masked": security.MaskAccessKeyID(p.AccessKeyID), "operations": ops, "notes": []string{"Inventory additionally queries traffic packages for discovered SWAS IDs; paginated reads may repeat up to 50 pages.", "CDT is an explicitly selected ListCdtInternetTraffic read probe; no DryRun or cloud-write operation is allowed."}, "cloud_requests": 0})
		return 0
	}
	var destination *os.File
	if *output != "" {
		path, e := externalPath(*output)
		if e != nil {
			return fail(e.Error())
		}
		if e = privateDir(filepath.Dir(path)); e != nil {
			return fail(e.Error())
		}
		destination, e = os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e != nil {
			return fail("report_path_exists_or_unavailable")
		}
		defer destination.Close()
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	httpClient := &http.Client{Transport: transport, Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer httpClient.CloseIdleConnections()
	caller := sdkCaller{client: httpClient}
	results := execute(ctx, p, ops, caller.call, 250*time.Millisecond)
	r := report{Account: p.Name, Check: *check, AccessKeyIDMasked: security.MaskAccessKeyID(p.AccessKeyID), Results: results, Notes: []string{"Read-only requests only; write permissions remain not_verified. CDT probe success is not a public API stability guarantee.", "fetched_at is retrieval time, not an official billing cutoff.", "Resources/IPs remain visible; credential fields and raw error messages are removed. Store reports outside the repository."}}
	w := out
	if destination != nil {
		w = destination
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if enc.Encode(r) != nil {
		return fail("report_write_failed")
	}
	if destination != nil {
		if destination.Sync() != nil {
			return fail("report_write_failed")
		}
		_ = json.NewEncoder(out).Encode(map[string]any{"status": "report_written", "account": p.Name, "cloud_writes": 0})
	}
	for _, item := range results {
		if item.Status != "passed" || !item.Complete {
			return 1
		}
	}
	if ctx.Err() != nil {
		return 1
	}
	return 0
}

type terminalIO struct {
	io.Reader
	io.Writer
}

func hiddenInput(ctx context.Context, terminal *term.Terminal, prompt string) ([]byte, error) {
	type result struct {
		value []byte
		err   error
	}
	done := make(chan result, 1)
	go func() { s, e := terminal.ReadPassword(prompt); done <- result{[]byte(s), e} }()
	select {
	case <-ctx.Done():
		return nil, errors.New("input_cancelled")
	case r := <-done:
		return r.value, r.err
	}
}
