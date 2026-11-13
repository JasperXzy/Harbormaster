package release

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/JasperXzy/harbormaster/internal/cloud/aliyun"
	"github.com/JasperXzy/harbormaster/internal/model"
	"github.com/JasperXzy/harbormaster/internal/security"
	"github.com/JasperXzy/harbormaster/internal/store"
)

type writerFunc func(context.Context, aliyun.Credentials, aliyun.ReleaseRequest) (aliyun.ReleaseResult, error)

func (f writerFunc) Execute(c context.Context, a aliyun.Credentials, r aliyun.ReleaseRequest) (aliyun.ReleaseResult, error) {
	return f(c, a, r)
}

type observerFuncs struct {
	inspect func(context.Context, aliyun.Credentials, string, string) (model.ReleaseInventoryObservation, error)
	observe func(context.Context, aliyun.Credentials, string, string) (model.ReleaseObservation, error)
}

func (f observerFuncs) Inspect(c context.Context, a aliyun.Credentials, r, id string) (model.ReleaseInventoryObservation, error) {
	return f.inspect(c, a, r, id)
}
func (f observerFuncs) Observe(c context.Context, a aliyun.Credentials, r, id string) (model.ReleaseObservation, error) {
	return f.observe(c, a, r, id)
}

func facts() model.ReleaseInventory {
	no, yes := false, true
	return model.ReleaseInventory{
		Instances: []model.ReleaseInstanceFacts{{CloudID: "i-test", Name: "exact name", Status: "Stopped", ChargeType: "PostPaid", DeletionProtection: &no, Locked: &no}},
		Disks:     []model.ReleaseDiskFacts{{CloudID: "d-test", Category: "cloud_essd", Type: "system", InstanceIDs: []string{"i-test"}, AttachmentsKnown: true, DeleteWithInstance: &yes, DeleteAutoSnapshot: &no}},
	}
}
func inspection(context.Context, aliyun.Credentials, string, string) (model.ReleaseInventoryObservation, error) {
	d := facts()
	return model.ReleaseInventoryObservation{Region: "cn-shanghai", QueriedAt: time.Now().UTC(), Data: &d}, nil
}
func fixture(t *testing.T) (*Engine, model.Job) {
	t.Helper()
	ctx := context.Background()
	s, err := store.Open(ctx, t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	box, err := security.NewBox(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	credential := model.Credential{AccessKeyID: "TestAccessKeyID123456", AccessKeySecret: "test-secret-not-real-at-all"}
	raw, _ := json.Marshal(credential)
	cipher, err := box.Encrypt(raw, model.CredentialOwner("a"))
	clear(raw)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if _, err := s.CreateAccount(ctx, model.Account{ID: "a", Name: "test", Site: "china", RegionIDs: []string{"cn-shanghai"}, Enabled: true, UpdatedAt: now}, cipher, time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec("INSERT INTO instance VALUES('instance','a','i-test','ecs','exact name','cn-shanghai','stopped','stopped','now')"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec("INSERT INTO resource_snapshot VALUES('a','ecs','instance','cn-shanghai','{}',1,'now')"); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(facts())
	if _, err := s.DB.Exec("INSERT INTO release_inventory VALUES('a',1,'cn-shanghai',?,?,NULL,?)", now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), string(data)); err != nil {
		t.Fatal(err)
	}
	preview, err := s.ReleasePreview(ctx, "instance", now)
	if err != nil || preview.Token == nil {
		t.Fatal(preview, err)
	}
	j, err := s.EnqueueReleaseJob(ctx, "instance", "exact name", *preview.Token, model.NewID("event_"), now)
	if err != nil {
		t.Fatal(err)
	}
	return &Engine{Store: s, Box: box, Enabled: true, PollInterval: time.Millisecond, ConfirmationTimeout: 150 * time.Millisecond}, j
}
func result(t *testing.T, e *Engine, j model.Job) (model.Job, model.ReleaseJobResult) {
	t.Helper()
	got, err := e.Store.Job(context.Background(), j.ID)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(got.Result)
	var r model.ReleaseJobResult
	if err := json.Unmarshal(raw, &r); err != nil {
		t.Fatal(err)
	}
	return got, r
}

func TestReleaseWorkerConfirmsAbsenceAfterOnePersistedDispatch(t *testing.T) {
	e, j := fixture(t)
	writes, reads := 0, 0
	e.Writer = writerFunc(func(ctx context.Context, cred aliyun.Credentials, req aliyun.ReleaseRequest) (aliyun.ReleaseResult, error) {
		writes++
		p, err := e.Store.ReleaseOperation(ctx, j.ID)
		if err != nil || p.DispatchStartedAt == nil || p.DispatchOutcome != "dispatching" || req.InstanceID != "i-test" || req.Region != "cn-shanghai" || cred.AccessKeyID != "TestAccessKeyID123456" {
			t.Fatal("write lacked persisted boundary or correct target")
		}
		return aliyun.ReleaseResult{Outcome: "accepted"}, nil
	})
	e.Observer = observerFuncs{inspect: inspection, observe: func(context.Context, aliyun.Credentials, string, string) (model.ReleaseObservation, error) {
		reads++
		if reads == 1 {
			return model.ReleaseObservation{}, errors.New("read did not prove absence")
		}
		return model.ReleaseObservation{InstancePresent: reads == 2, ObservedAt: time.Now().UTC()}, nil
	}}
	if did, err := e.RunOne(context.Background()); err != nil || !did {
		t.Fatal(did, err)
	}
	got, r := result(t, e, j)
	if got.Status != "succeeded" || r.DispatchOutcome != "accepted" || r.ConfirmationBasis == nil || *r.ConfirmationBasis != "not_found_after_accept" || r.Observation == nil || r.Observation.InstancePresent || writes != 1 || reads != 3 {
		t.Fatal(got, r, writes, reads)
	}
}

func TestReleaseWorkerBlocksChangedOrUnavailablePreflight(t *testing.T) {
	for _, name := range []string{"renamed", "running", "disk changed", "read failed", "missing", "cancelled"} {
		t.Run(name, func(t *testing.T) {
			e, j := fixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			e.Writer = writerFunc(func(context.Context, aliyun.Credentials, aliyun.ReleaseRequest) (aliyun.ReleaseResult, error) {
				t.Fatal("blocked request reached writer")
				return aliyun.ReleaseResult{}, nil
			})
			e.Observer = observerFuncs{inspect: func(c context.Context, a aliyun.Credentials, r, id string) (model.ReleaseInventoryObservation, error) {
				o, _ := inspection(c, a, r, id)
				switch name {
				case "renamed":
					o.Data.Instances[0].Name = "new name"
				case "running":
					o.Data.Instances[0].Status = "Running"
				case "disk changed":
					v := false
					o.Data.Disks[0].DeleteWithInstance = &v
				case "read failed":
					return o, errors.New("private transport data")
				case "missing":
					return o, &aliyun.Error{Code: "instance_not_found"}
				case "cancelled":
					cancel()
				}
				return o, nil
			}, observe: func(context.Context, aliyun.Credentials, string, string) (model.ReleaseObservation, error) {
				t.Fatal("unaccepted request was observed")
				return model.ReleaseObservation{}, nil
			}}
			if did, err := e.RunOne(ctx); err != nil || !did {
				t.Fatal(did, err)
			}
			got, r := result(t, e, j)
			if got.Status != "failed" || r.DispatchOutcome != "not_sent" {
				t.Fatal(got, r)
			}
		})
	}
}

func TestReleaseWorkerNeverReplaysUnknownOrRejectedWrites(t *testing.T) {
	for _, outcome := range []string{"unknown", "rejected", "invalid", "invalid_code"} {
		t.Run(outcome, func(t *testing.T) {
			e, j := fixture(t)
			writes := 0
			e.Writer = writerFunc(func(context.Context, aliyun.Credentials, aliyun.ReleaseRequest) (aliyun.ReleaseResult, error) {
				writes++
				code := "release_outcome_unknown"
				if outcome == "rejected" {
					code = "instance_deletion_protected"
				}
				if outcome == "invalid_code" {
					return aliyun.ReleaseResult{Outcome: "rejected"}, &aliyun.Error{Code: "raw-provider-text-must-not-be-persisted"}
				}
				return aliyun.ReleaseResult{Outcome: outcome}, &aliyun.Error{Code: code}
			})
			e.Observer = observerFuncs{inspect: inspection, observe: func(context.Context, aliyun.Credentials, string, string) (model.ReleaseObservation, error) {
				t.Fatal("unaccepted write was observed")
				return model.ReleaseObservation{}, nil
			}}
			if _, err := e.RunOne(context.Background()); err != nil {
				t.Fatal(err)
			}
			if did, err := e.RunOne(context.Background()); err != nil || did {
				t.Fatal(did, err)
			}
			got, r := result(t, e, j)
			if got.Status != "failed" || writes != 1 || (outcome != "rejected" && r.DispatchOutcome != "unknown") {
				t.Fatal(got, r, writes)
			}
		})
	}
}

func TestReleaseWorkerReadErrorsAndCancellationPreserveAcceptedEvidence(t *testing.T) {
	for _, stop := range []bool{false, true} {
		t.Run(map[bool]string{false: "timeout", true: "shutdown"}[stop], func(t *testing.T) {
			e, j := fixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			e.ConfirmationTimeout = 30 * time.Millisecond
			e.Writer = writerFunc(func(context.Context, aliyun.Credentials, aliyun.ReleaseRequest) (aliyun.ReleaseResult, error) {
				if stop {
					cancel()
				}
				return aliyun.ReleaseResult{Outcome: "accepted"}, nil
			})
			e.Observer = observerFuncs{inspect: inspection, observe: func(context.Context, aliyun.Credentials, string, string) (model.ReleaseObservation, error) {
				return model.ReleaseObservation{}, errors.New("unknown response")
			}}
			if _, err := e.RunOne(ctx); err != nil {
				t.Fatal(err)
			}
			got, r := result(t, e, j)
			want := "release_confirmation_timeout"
			if stop {
				want = "release_confirmation_interrupted"
			}
			if got.Status != "failed" || got.Error.Code != want || r.DispatchOutcome != "accepted" || r.ConfirmationBasis != nil || r.Observation != nil {
				t.Fatal(got, r)
			}
			var present int
			if err := e.Store.DB.QueryRow("SELECT present FROM resource_snapshot WHERE id='instance'").Scan(&present); err != nil || present != 1 {
				t.Fatal("read errors became absence", present, err)
			}
		})
	}
}

func TestReleaseWorkerDisabledAndWrongMasterNeverReadCloud(t *testing.T) {
	e, j := fixture(t)
	e.Enabled = false
	if did, err := e.RunOne(context.Background()); err != nil || did {
		t.Fatal(did, err)
	}
	if got, _ := result(t, e, j); got.Status != "queued" {
		t.Fatal(got)
	}
	e.Enabled = true
	e.Box, _ = security.NewBox(bytes.Repeat([]byte{8}, 32))
	e.Writer = writerFunc(func(context.Context, aliyun.Credentials, aliyun.ReleaseRequest) (aliyun.ReleaseResult, error) {
		t.Fatal("invalid credentials reached writer")
		return aliyun.ReleaseResult{}, nil
	})
	e.Observer = observerFuncs{inspect: func(context.Context, aliyun.Credentials, string, string) (model.ReleaseInventoryObservation, error) {
		t.Fatal("invalid credentials reached cloud read")
		return model.ReleaseInventoryObservation{}, nil
	}}
	if _, err := e.RunOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got, r := result(t, e, j); got.Status != "failed" || got.Error.Code != "credential_decryption_failed" || r.DispatchOutcome != "not_sent" {
		t.Fatal(got, r)
	}
}

func TestReleaseWorkerFencesAccountChangesBeforeAndAfterDispatch(t *testing.T) {
	for _, after := range []bool{false, true} {
		t.Run(map[bool]string{false: "before dispatch", true: "after acceptance"}[after], func(t *testing.T) {
			e, j := fixture(t)
			change := func() {
				_, err := e.Store.UpdateAccount(context.Background(), "a", func(a *model.Account) ([]byte, error) {
					a.Name = "updated account"
					return nil, nil
				}, time.Now().UTC(), time.Hour)
				if err != nil {
					t.Fatal(err)
				}
			}
			writes := 0
			e.Observer = observerFuncs{inspect: func(c context.Context, a aliyun.Credentials, r, id string) (model.ReleaseInventoryObservation, error) {
				o, err := inspection(c, a, r, id)
				if !after {
					change()
				}
				return o, err
			}, observe: func(context.Context, aliyun.Credentials, string, string) (model.ReleaseObservation, error) {
				t.Fatal("changed account continued confirmation")
				return model.ReleaseObservation{}, nil
			}}
			e.Writer = writerFunc(func(context.Context, aliyun.Credentials, aliyun.ReleaseRequest) (aliyun.ReleaseResult, error) {
				writes++
				if !after {
					t.Fatal("changed account reached writer")
				}
				change()
				return aliyun.ReleaseResult{Outcome: "accepted"}, nil
			})
			if _, err := e.RunOne(context.Background()); err != nil {
				t.Fatal(err)
			}
			got, result := result(t, e, j)
			wantOutcome, wantCode := "not_sent", "account_changed"
			if after {
				wantOutcome, wantCode = "accepted", "release_confirmation_interrupted"
			}
			if got.Status != "failed" || got.Error.Code != wantCode || result.DispatchOutcome != wantOutcome || (after && writes != 1) || (!after && writes != 0) {
				t.Fatal(got, result, writes)
			}
		})
	}
}
