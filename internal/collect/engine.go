package collect

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
	"github.com/JasperXzy/harbormaster/internal/security"
	"github.com/JasperXzy/harbormaster/internal/store"
)

type ValidationError struct{ Fields map[string]string }

func (e *ValidationError) Error() string { return "validation_failed" }

type Scanner interface {
	Scan(context.Context, model.Account, model.Credential, bool, func() error) model.ScanResult
}
type TrafficScanner interface {
	Traffic(context.Context, model.Account, model.Credential, func() error) model.CDTObservation
}
type FinanceScanner interface {
	Finance(context.Context, model.Account, model.Credential, string, func() error) []model.FinanceObservation
}
type SystemEventScanner interface {
	SystemEvents(context.Context, model.Account, model.Credential, func() error) []model.SystemEventObservation
}

type Engine struct {
	schedulerProgress   atomic.Int64
	SystemEventInterval time.Duration
	Store               *store.Store
	Box                 *security.Box
	Scanner             Scanner
	Workers             int
	Interval            time.Duration
	CDTInterval         time.Duration
	Log                 *slog.Logger
	Timezone            string
}

func (e *Engine) encrypt(id string, c model.Credential) ([]byte, error) {
	if e.Box == nil {
		return nil, errors.New("credential_store_unavailable")
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return nil, err
	}
	defer clear(raw)
	return e.Box.Encrypt(raw, model.CredentialOwner(id))
}
func (e *Engine) CreateAccount(ctx context.Context, in model.AccountCreate) (model.Account, model.Job, error) {
	now := time.Now().UTC()
	a := model.Account{ID: model.NewID("acct_"), Name: strings.TrimSpace(in.Name), Site: in.Site, RegionIDs: in.RegionIDs, Enabled: true, QuotaMainland: 20 << 30, QuotaOverseas: 200 << 30, ResetDay: 1, Thresholds: []int{80, 90, 95}, SyncStatus: "never_synced", UpdatedAt: now, Revision: 1}
	model.AccountUpdate{QuotaMainland: in.QuotaMainland, QuotaOverseas: in.QuotaOverseas, ResetDay: in.ResetDay, Thresholds: in.Thresholds}.Apply(&a)
	f := a.Validate()
	for k, v := range in.Credential.Validate() {
		f[k] = v
	}
	if len(f) > 0 {
		return a, model.Job{}, &ValidationError{f}
	}
	a.AccessKeyIDMasked = security.MaskAccessKeyID(in.AccessKeyID)
	cipher, err := e.encrypt(a.ID, in.Credential)
	if err != nil {
		return a, model.Job{}, err
	}
	j, err := e.Store.CreateAccount(ctx, a, cipher, e.scanInterval())
	return a, j, err
}
func (e *Engine) UpdateAccount(ctx context.Context, id string, in model.AccountUpdate) (model.Account, error) {
	return e.Store.UpdateAccount(ctx, id, func(a *model.Account) ([]byte, error) {
		wasEnabled := a.Enabled
		in.Apply(a)
		f := a.Validate()
		if (in.AccessKeyID == nil) != (in.AccessKeySecret == nil) {
			f["access_key_id"] = "required"
			f["access_key_secret"] = "required"
		}
		var c model.Credential
		if in.AccessKeyID != nil && in.AccessKeySecret != nil {
			c = model.Credential{AccessKeyID: *in.AccessKeyID, AccessKeySecret: *in.AccessKeySecret}
			for k, v := range c.Validate() {
				f[k] = v
			}
		}
		if len(f) > 0 {
			return nil, &ValidationError{f}
		}
		if in.AccessKeyID != nil {
			a.AccessKeyIDMasked = security.MaskAccessKeyID(c.AccessKeyID)
			a.PermissionCheck = nil
			a.SyncStatus = "never_synced"
			a.ConsecutiveFailures = 0
			a.LastError = nil
			return e.encrypt(a.ID, c)
		}
		if in.RegionIDs != nil || (!wasEnabled && a.Enabled) {
			a.PermissionCheck = nil
			a.SyncStatus = "never_synced"
			a.ConsecutiveFailures = 0
			a.LastError = nil
		}
		return nil, nil
	}, time.Now().UTC(), e.scanInterval())
}
func (e *Engine) scanInterval() time.Duration {
	if e.Interval < time.Minute {
		return 5 * time.Minute
	}
	return e.Interval
}
func (e *Engine) TrafficInterval() time.Duration {
	if e.CDTInterval < time.Minute {
		return 5 * time.Minute
	}
	return e.CDTInterval
}
func (e *Engine) SystemEventPollInterval() time.Duration {
	if e.SystemEventInterval < 15*time.Second {
		return time.Minute
	}
	return e.SystemEventInterval
}
func (e *Engine) Schedule(ctx context.Context) error {
	if err := e.Store.ScheduleScans(ctx, e.scanInterval(), time.Now().UTC()); err != nil {
		return err
	}
	if _, ok := e.Scanner.(TrafficScanner); ok {
		if err := e.Store.ScheduleCDT(ctx, e.TrafficInterval(), time.Now().UTC()); err != nil {
			return err
		}
	}
	if _, ok := e.Scanner.(SystemEventScanner); ok {
		return e.Store.ScheduleSystemEvents(ctx, e.SystemEventPollInterval(), time.Now().UTC())
	}
	return nil
}
func (e *Engine) Enqueue(ctx context.Context, kind, id string) (model.Job, error) {
	ids := []string{}
	scope := id
	if id != "" {
		ids = append(ids, id)
	} else {
		if kind != "refresh" {
			return model.Job{}, store.ErrNotFound
		}
		scope = "all"
		accounts, err := e.Store.Accounts(ctx)
		if err != nil {
			return model.Job{}, err
		}
		for _, a := range accounts {
			if a.Enabled {
				ids = append(ids, a.ID)
			}
		}
	}
	return e.Store.EnqueueJob(ctx, kind, scope, ids, time.Now().UTC())
}

// Run joins every worker on shutdown. Context cancellation leaves a read job
// recoverable; no cloud result is accepted after its database lease expires.
func (e *Engine) Run(ctx context.Context) {
	workers := max(1, e.Workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ticker := time.NewTicker(time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				default:
				}
				if _, err := e.RunOne(ctx); err != nil && !errors.Is(err, store.ErrLeaseLost) && ctx.Err() == nil && e.Log != nil {
					e.Log.Warn("worker_step_failed")
				}
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
				}
			}
		}()
	}
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		err := e.Schedule(ctx)
		if err == nil {
			e.schedulerProgress.Store(time.Now().UnixNano())
		} else if ctx.Err() == nil && e.Log != nil {
			e.Log.Warn("schedule_failed")
		}
		select {
		case <-ctx.Done():
			wg.Wait()
			return
		case <-ticker.C:
		}
	}
}
func (e *Engine) RunOne(ctx context.Context) (bool, error) {
	j, err := e.Store.ClaimCollectionJob(ctx, time.Now().UTC())
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	jobCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	heartbeatDone := make(chan struct{})
	go func() {
		defer close(heartbeatDone)
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-jobCtx.Done():
				return
			case <-ticker.C:
				if e.Store.RenewJob(jobCtx, j, time.Now().UTC()) != nil {
					cancel()
					return
				}
			}
		}
	}()
	defer func() { cancel(); <-heartbeatDone }()
	if j.Type != "refresh" && j.Type != "permission_check" {
		return true, e.Store.FinishJob(ctx, j, nil, "not_implemented", time.Now().UTC())
	}
	var result any
	failure := ""
	for _, id := range j.AccountIDs {
		if jobCtx.Err() != nil {
			break
		}
		a, err := e.Store.BeginAccountScan(jobCtx, id, j.Owner, time.Now().UTC())
		if errors.Is(err, store.ErrAccountBusy) {
			return true, e.Store.RetryJob(ctx, j, 2*time.Second, true, time.Now().UTC())
		}
		if errors.Is(err, store.ErrAccountDisabled) || errors.Is(err, store.ErrNotFound) {
			if j.Type == "permission_check" {
				failure = "account_disabled"
			}
			continue
		}
		if err != nil {
			failure = "collection_failed"
			continue
		}
		scan, err := e.scanAccount(jobCtx, j, a)
		if scan.Permissions != nil {
			result = scan.Permissions
		}
		if err != nil {
			failure = "account_changed"
			if !errors.Is(err, store.ErrAccountChanged) && !errors.Is(err, context.Canceled) {
				failure = "collection_failed"
			}
			continue
		}
		if scan.Failure != "" {
			failure = scan.Failure
		}
	}
	if jobCtx.Err() != nil {
		// The request context may be cancelled; use a bounded local cleanup context.
		cleanup, stop := context.WithTimeout(context.Background(), 3*time.Second)
		defer stop()
		return true, e.Store.RetryJob(cleanup, j, time.Second, false, time.Now().UTC())
	}
	if j.Type != "permission_check" {
		result = nil
	}
	if (failure == "aliyun_throttled" || failure == "aliyun_unreachable") && j.Attempts < 3 {
		return true, e.Store.RetryJob(ctx, j, time.Duration(j.Attempts*10)*time.Second, false, time.Now().UTC())
	}
	return true, e.Store.FinishJob(ctx, j, result, failure, time.Now().UTC())
}
func (e *Engine) scanAccount(ctx context.Context, j model.Job, a model.Account) (model.ScanResult, error) {
	scanCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 3*time.Second)
		defer stop()
		_ = e.Store.ReleaseAccountScan(cleanup, a.ID, j.Owner)
	}()
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-scanCtx.Done():
				return
			case <-ticker.C:
				if e.Store.RenewAccountScan(scanCtx, a.ID, j.Owner, a.Revision, time.Now().UTC()) != nil {
					cancel()
					return
				}
			}
		}
	}()
	defer func() { cancel(); <-done }()
	cipher, err := e.Store.AccountCipher(scanCtx, a.ID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			result := model.ScanResult{Failure: "credential_unavailable"}
			return result, e.Store.SaveScan(scanCtx, j, a, result, time.Now().UTC())
		}
		return model.ScanResult{}, err
	}
	raw, err := e.Box.Decrypt(cipher, model.CredentialOwner(a.ID))
	if err != nil {
		result := model.ScanResult{Failure: "credential_decryption_failed"}
		return result, e.Store.SaveScan(scanCtx, j, a, result, time.Now().UTC())
	}
	defer clear(raw)
	var credential model.Credential
	if json.Unmarshal(raw, &credential) != nil {
		result := model.ScanResult{Failure: "credential_decryption_failed"}
		return result, e.Store.SaveScan(scanCtx, j, a, result, time.Now().UTC())
	}
	before := func() error {
		if scanCtx.Err() != nil {
			return scanCtx.Err()
		}
		return e.Store.CheckAccountScan(scanCtx, a.ID, j.Owner, a.Revision, time.Now().UTC())
	}
	if strings.HasPrefix(j.Scope, "system-events:") {
		scanner, ok := e.Scanner.(SystemEventScanner)
		if !ok {
			return model.ScanResult{Failure: "not_implemented"}, nil
		}
		observations := scanner.SystemEvents(scanCtx, a, credential, before)
		result := model.ScanResult{}
		if len(observations) != len(a.RegionIDs)*2 {
			result.Failure = "system_event_query_incomplete"
		}
		for _, o := range observations {
			if o.Error != nil && result.Failure == "" {
				result.Failure = o.Error.Code
			}
		}
		if err := before(); err != nil {
			return result, err
		}
		return result, e.Store.SaveSystemEvents(scanCtx, j, a, observations, time.Now().UTC())
	}
	if strings.HasPrefix(j.Scope, "cdt:") {
		scanner, ok := e.Scanner.(TrafficScanner)
		if !ok {
			return model.ScanResult{Failure: "not_implemented"}, nil
		}
		o := scanner.Traffic(scanCtx, a, credential, before)
		result := model.ScanResult{CDT: &o}
		if o.Error != nil {
			result.Failure = o.Error.Code
		}
		if err := before(); err != nil {
			return result, err
		}
		return result, e.Store.SaveCDT(scanCtx, j, a, o, time.Now().UTC())
	}
	result := e.Scanner.Scan(scanCtx, a, credential, j.Type == "permission_check", before)
	if scanner, ok := e.Scanner.(SystemEventScanner); ok && j.Type == "permission_check" {
		observations := scanner.SystemEvents(scanCtx, a, credential, before)
		if err := before(); err != nil {
			return result, err
		}
		if err := e.Store.SaveSystemEvents(scanCtx, j, a, observations, time.Now().UTC()); err != nil {
			return result, err
		}
		for _, o := range observations {
			key := "ecs_read"
			if o.Source == "cms_spot" {
				key = "cms_read"
			}
			if o.Error != nil {
				if result.Failure == "" {
					result.Failure = o.Error.Code
				}
				if result.Permissions != nil {
					for i := range result.Permissions.Items {
						item := &result.Permissions.Items[i]
						if item.Key == key && item.Result != "missing" {
							item.Result = "check_failed"
							if o.Error.Code == "permission_denied" {
								item.Result = "missing"
							}
						}
					}
				}
			}
		}
	}

	if scanner, ok := e.Scanner.(FinanceScanner); ok {
		loc, err := e.Store.BusinessLocation(scanCtx, e.Timezone)
		if err != nil {
			return result, err
		}
		result.Finance = scanner.Finance(scanCtx, a, credential, time.Now().In(loc).Format("2006-01"), before)
		permission := "passed"
		for _, source := range result.Finance {
			if source.Error == nil {
				continue
			}
			if result.Failure == "" {
				result.Failure = source.Error.Code
			}
			if source.Error.Code == "permission_denied" {
				permission = "missing"
			} else if permission != "missing" {
				permission = "check_failed"
			}
		}
		if result.Permissions != nil {
			for i := range result.Permissions.Items {
				if result.Permissions.Items[i].Key == "bss_read" {
					old := result.Permissions.Items[i].Result
					if old == "missing" || permission == "missing" {
						result.Permissions.Items[i].Result = "missing"
					} else if old == "check_failed" || permission == "check_failed" {
						result.Permissions.Items[i].Result = "check_failed"
					} else {
						result.Permissions.Items[i].Result = permission
					}
				}
			}
		}
	}
	// An explicit refresh covers both resources and CDT; scheduled resource
	// scans leave CDT to its independently configured poll interval.
	if result.CDT == nil && !strings.HasPrefix(j.Scope, "inventory:") {
		if scanner, ok := e.Scanner.(TrafficScanner); ok {
			o := scanner.Traffic(scanCtx, a, credential, before)
			result.CDT = &o
			if o.Error != nil && result.Failure == "" {
				result.Failure = o.Error.Code
			}
		}
	}
	if err := before(); err != nil {
		return result, err
	}
	return result, e.Store.SaveScan(scanCtx, j, a, result, time.Now().UTC())
}

// SchedulerProgress is independent of individual cloud account outcomes. A
// blocked/failed scheduler cannot keep renewing this liveness evidence.
func (e *Engine) SchedulerProgress() time.Time {
	n := e.schedulerProgress.Load()
	if n == 0 {
		return time.Time{}
	}
	return time.Unix(0, n).UTC()
}
