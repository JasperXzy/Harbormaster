// Package release executes explicitly confirmed, persisted ECS release jobs.
// It has no schedule, discovery or production startup binding of its own.
package release

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
	Execute(context.Context, aliyun.Credentials, aliyun.ReleaseRequest) (aliyun.ReleaseResult, error)
}
type Observer interface {
	Inspect(context.Context, aliyun.Credentials, string, string) (model.ReleaseInventoryObservation, error)
	Observe(context.Context, aliyun.Credentials, string, string) (model.ReleaseObservation, error)
}

type Engine struct {
	Store                             *store.Store
	Box                               *security.Box
	Writer                            Writer
	Observer                          Observer
	Enabled                           bool
	Workers                           int
	PollInterval, ConfirmationTimeout time.Duration
	Log                               *slog.Logger
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

func (e *Engine) Run(ctx context.Context) {
	if !e.Enabled {
		return
	}
	workers := max(1, min(e.Workers, 8))
	var wg sync.WaitGroup
	for range workers {
		wg.Go(func() {
			for ctx.Err() == nil {
				_, err := e.RunOne(ctx)
				if err != nil && ctx.Err() == nil && !errors.Is(err, store.ErrLeaseLost) && e.Log != nil {
					e.Log.Warn("release_worker_step_failed")
				}
				if !wait(ctx, time.Second) {
					return
				}
			}
		})
	}
	wg.Wait()
}

func (e *Engine) RunOne(ctx context.Context) (bool, error) {
	if !e.Enabled {
		return false, nil
	}
	if e.Store == nil || e.Box == nil || e.Writer == nil || e.Observer == nil {
		return false, errors.New("release_worker_unavailable")
	}
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	p, err := e.Store.ClaimReleaseJob(ctx, time.Now().UTC())
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
		return true, cleanup(func(c context.Context) error { return e.Store.FailReleaseJob(c, j, code, time.Now().UTC()) })
	}
	a, cipher, err := e.Store.ReleaseCredentials(jobCtx, j, time.Now().UTC())
	if err != nil {
		code := "release_preflight_failed"
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
	inspectCtx, stopInspect := context.WithTimeout(jobCtx, 30*time.Second)
	fresh, err := e.Observer.Inspect(inspectCtx, cred, p.Region, p.CloudInstanceID)
	stopInspect()
	if err != nil {
		if jobCtx.Err() != nil {
			return fail("cancelled")
		}
		if aliyun.Code(err) == "instance_not_found" {
			return fail("instance_not_found")
		}
		return fail("release_preflight_failed")
	}
	if jobCtx.Err() != nil {
		return fail("cancelled")
	}
	p, err = e.Store.BeginReleaseDispatch(jobCtx, j, fresh, time.Now().UTC())
	if err != nil {
		if errors.Is(err, store.ErrAccountChanged) || errors.Is(err, store.ErrAccountDisabled) || errors.Is(err, store.ErrNotFound) || errors.Is(err, store.ErrReleaseInventory) || errors.Is(err, store.ErrReleasePreviewChanged) {
			return true, nil
		}
		return true, err
	}
	defer func() {
		_ = cleanup(func(c context.Context) error {
			_, err := e.Store.EnqueueJob(model.WithJobTrigger(c, "system"), "refresh", "inventory:"+p.AccountID, []string{p.AccountID}, time.Now().UTC())
			return err
		})
	}()
	var result aliyun.ReleaseResult
	if jobCtx.Err() != nil {
		result, err = aliyun.ReleaseResult{Outcome: "not_sent"}, &aliyun.Error{Code: "cancelled"}
	} else {
		result, err = e.Writer.Execute(jobCtx, cred, aliyun.ReleaseRequest{Region: p.Region, InstanceID: p.CloudInstanceID})
	}
	code := aliyun.Code(err)
	switch result.Outcome {
	case "accepted":
		if err != nil {
			result.Outcome, code = "unknown", "release_outcome_unknown"
		}
	case "not_sent", "rejected":
		if err == nil {
			result.Outcome, code = "unknown", "release_outcome_unknown"
		}
	case "unknown":
		code = "release_outcome_unknown"
	default:
		result.Outcome, code = "unknown", "release_outcome_unknown"
	}
	if err := cleanup(func(c context.Context) error {
		err := e.Store.RecordReleaseDispatch(c, j, result.Outcome, code, time.Now().UTC())
		if errors.Is(err, store.ErrReleaseParameters) {
			result.Outcome = "unknown"
			return e.Store.RecordReleaseDispatch(c, j, "unknown", "release_outcome_unknown", time.Now().UTC())
		}
		return err
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
	observeCtx, stopObserve := context.WithTimeout(jobCtx, timeout)
	defer stopObserve()
	for observeCtx.Err() == nil {
		if _, _, err := e.Store.ReleaseCredentials(observeCtx, j, time.Now().UTC()); err != nil {
			if observeCtx.Err() != nil {
				break
			}
			return fail("release_confirmation_interrupted")
		}
		observation, readErr := e.Observer.Observe(observeCtx, cred, p.Region, p.CloudInstanceID)
		if readErr == nil {
			finished, saveErr := e.Store.ObserveRelease(observeCtx, j, observation, time.Now().UTC())
			if saveErr != nil && observeCtx.Err() == nil {
				if errors.Is(saveErr, store.ErrAccountChanged) || errors.Is(saveErr, store.ErrAccountDisabled) {
					return fail("release_confirmation_interrupted")
				}
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
	code = "release_confirmation_timeout"
	if jobCtx.Err() != nil {
		code = "release_confirmation_interrupted"
	}
	return fail(code)
}
