package collect

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
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
type Engine struct {
	Store    *store.Store
	Box      *security.Box
	Scanner  Scanner
	Workers  int
	Interval time.Duration
	Log      *slog.Logger
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
			return e.encrypt(a.ID, c)
		}
		if in.RegionIDs != nil {
			a.PermissionCheck = nil
			a.SyncStatus = "never_synced"
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
	interval := e.Interval
	if interval < time.Minute {
		interval = 5 * time.Minute
	}
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		if err := e.Store.ScheduleScans(ctx, interval, time.Now().UTC()); err != nil && ctx.Err() == nil && e.Log != nil {
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
	j, err := e.Store.ClaimJob(ctx, time.Now().UTC())
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
		return model.ScanResult{}, err
	}
	raw, err := e.Box.Decrypt(cipher, model.CredentialOwner(a.ID))
	if err != nil {
		return model.ScanResult{}, errors.New("credential_decryption_failed")
	}
	defer clear(raw)
	var credential model.Credential
	if json.Unmarshal(raw, &credential) != nil {
		return model.ScanResult{}, errors.New("credential_decryption_failed")
	}
	before := func() error {
		if scanCtx.Err() != nil {
			return scanCtx.Err()
		}
		return e.Store.CheckAccountScan(scanCtx, a.ID, j.Owner, a.Revision, time.Now().UTC())
	}
	result := e.Scanner.Scan(scanCtx, a, credential, j.Type == "permission_check", before)
	if err := before(); err != nil {
		return result, err
	}
	return result, e.Store.SaveScan(scanCtx, j, a, result, time.Now().UTC())
}
