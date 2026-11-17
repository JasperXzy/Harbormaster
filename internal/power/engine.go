// Package power executes persisted, fixed instance power operations. It is
// separate from collection workers and has no periodic action scheduler.
package power

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/JasperXzy/harbormaster/internal/cloud/aliyun"
	"github.com/JasperXzy/harbormaster/internal/model"
	"github.com/JasperXzy/harbormaster/internal/security"
	"github.com/JasperXzy/harbormaster/internal/store"
)

type Writer interface {
	Execute(context.Context, aliyun.Credentials, aliyun.PowerRequest) (aliyun.PowerResult, error)
}
type Observer interface {
	Observe(context.Context, aliyun.Credentials, string, string, string) (model.PowerObservation, error)
}

type Engine struct {
	ProtectionPolicy                  model.ProtectionPolicy
	Store                             *store.Store
	Box                               *security.Box
	Writer                            Writer
	Observer                          Observer
	Enabled                           bool
	Workers                           int
	PollInterval, ConfirmationTimeout time.Duration
	Log                               *slog.Logger
}

func (e *Engine) Run(ctx context.Context) {
	if !e.Enabled {
		return
	}
	workers := e.Workers
	if workers < 1 {
		workers = 1
	}
	if workers > 8 {
		workers = 8
	}
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ctx.Err() == nil {
				_, err := e.RunOne(ctx)
				if err != nil && ctx.Err() == nil && !errors.Is(err, store.ErrLeaseLost) && e.Log != nil {
					e.Log.Warn("power_worker_step_failed")
				}
				if !wait(ctx, time.Second) {
					return
				}
			}
		}()
	}
	wg.Wait()
}
func wait(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return ctx.Err() == nil
	}
}
func cleanup(fn func(context.Context) error) error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return fn(ctx)
}

func (e *Engine) RunOne(ctx context.Context) (bool, error) {
	if !e.Enabled {
		return false, nil
	}
	if e.Store == nil || e.Box == nil || e.Writer == nil || e.Observer == nil {
		return false, errors.New("power_worker_unavailable")
	}
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	p, err := e.Store.ClaimPowerJob(ctx, time.Now().UTC())
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	j := p.Job
	jobCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
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
	defer func() { cancel(); <-done }()
	fail := func(code string) (bool, error) {
		return true, cleanup(func(c context.Context) error { return e.Store.FailPowerJob(c, p.Job, code, time.Now().UTC()) })
	}
	a, cipher, err := e.Store.PowerCredentials(jobCtx, p.Job, time.Now().UTC())
	if err != nil {
		code := "power_preflight_failed"
		if errors.Is(err, store.ErrAccountChanged) {
			code = "account_changed"
		}
		if errors.Is(err, store.ErrAccountDisabled) {
			code = "account_disabled"
		}
		if jobCtx.Err() != nil {
			code = "cancelled"
		}
		return fail(code)
	}
	raw, err := e.Box.Decrypt(cipher, model.CredentialOwner(a.ID))
	if err != nil {
		return fail("credential_decryption_failed")
	}
	var credential model.Credential
	err = json.Unmarshal(raw, &credential)
	clear(raw)
	if err != nil || len(credential.Validate()) != 0 {
		return fail("credential_decryption_failed")
	}
	cred := aliyun.Credentials{Credential: credential, Site: a.Site, Regions: a.RegionIDs}
	before, err := e.Observer.Observe(jobCtx, cred, p.Kind, p.Region, p.CloudInstanceID)
	if err != nil {
		if jobCtx.Err() != nil {
			return fail("cancelled")
		}
		if aliyun.Code(err) == "instance_not_found" {
			return fail("instance_not_found")
		}
		return fail("power_preflight_failed")
	}
	if err = e.Store.SavePowerBaseline(jobCtx, p.Job, before, time.Now().UTC()); err != nil {
		return fail("power_preflight_failed")
	}
	expected := "running"
	if p.Action == "start" {
		expected = "stopped"
	}
	if before.Status != expected {
		return fail("instance_state_conflict")
	}
	if jobCtx.Err() != nil {
		return fail("cancelled")
	}
	p, err = e.Store.BeginPowerDispatchWithPolicy(jobCtx, p.Job, e.ProtectionPolicy, time.Now().UTC())
	if err != nil {
		if errors.Is(err, store.ErrProtectionGuard) || errors.Is(err, store.ErrAccountChanged) || errors.Is(err, store.ErrAccountDisabled) || errors.Is(err, store.ErrNotFound) {
			return true, nil
		}
		return true, err
	}
	// Refresh after every possible write, including uncertain outcomes, so later
	// inventory can discover status/IP changes without asserting causality.
	defer func() {
		_ = cleanup(func(c context.Context) error {
			_, err := e.Store.EnqueueJob(model.WithJobTrigger(c, "system"), "refresh", "inventory:"+p.AccountID, []string{p.AccountID}, time.Now().UTC())
			return err
		})
	}()
	req := aliyun.PowerRequest{Kind: p.Kind, Region: p.Region, InstanceID: p.CloudInstanceID, Action: p.Action, StopMode: p.StopMode}
	if p.Kind == "swas" {
		req.ClientToken = p.Job.ID
	}
	var result aliyun.PowerResult
	if jobCtx.Err() != nil {
		result = aliyun.PowerResult{Outcome: "not_sent"}
		err = &aliyun.Error{Code: "cancelled"}
	} else {
		result, err = e.Writer.Execute(jobCtx, cred, req)
	}
	code := aliyun.Code(err)
	if result.Outcome == "unknown" {
		code = "power_outcome_unknown"
	}
	if result.Outcome == "" || result.Outcome == "accepted" && err != nil {
		result.Outcome, code = "unknown", "power_outcome_unknown"
	}
	if err = cleanup(func(c context.Context) error {
		return e.Store.RecordPowerDispatch(c, p.Job, result.Outcome, code, time.Now().UTC())
	}); err != nil {
		return true, err
	}
	if result.Outcome != "accepted" {
		return true, nil
	}
	interval, timeout := e.PollInterval, e.ConfirmationTimeout
	if interval <= 0 {
		interval = 5 * time.Second
	}
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	observeCtx, stop := context.WithTimeout(jobCtx, timeout)
	defer stop()
	for observeCtx.Err() == nil {
		if _, _, err = e.Store.PowerCredentials(observeCtx, p.Job, time.Now().UTC()); err != nil {
			if observeCtx.Err() != nil {
				break
			}
			return fail("power_confirmation_interrupted")
		}
		observation, readErr := e.Observer.Observe(observeCtx, cred, p.Kind, p.Region, p.CloudInstanceID)
		if readErr == nil {
			finished, saveErr := e.Store.ObservePower(observeCtx, p.Job, observation, time.Now().UTC())
			if saveErr != nil && observeCtx.Err() == nil {
				return true, saveErr
			}
			if finished {
				return true, nil
			}
		}
		if !wait(observeCtx, interval) {
			break
		}
	}
	code = "power_confirmation_timeout"
	if jobCtx.Err() != nil {
		code = "power_confirmation_interrupted"
	}
	return fail(code)
}
