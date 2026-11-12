package power

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/JasperXzy/harbormaster/internal/cloud/aliyun"
	"github.com/JasperXzy/harbormaster/internal/model"
	"github.com/JasperXzy/harbormaster/internal/security"
	"github.com/JasperXzy/harbormaster/internal/store"
)

type writerFunc func(context.Context, aliyun.Credentials, aliyun.PowerRequest) (aliyun.PowerResult, error)

func (f writerFunc) Execute(c context.Context, a aliyun.Credentials, r aliyun.PowerRequest) (aliyun.PowerResult, error) {
	return f(c, a, r)
}

type observerFunc func(context.Context, aliyun.Credentials, string, string, string) (model.PowerObservation, error)

func (f observerFunc) Observe(c context.Context, a aliyun.Credentials, k, r, id string) (model.PowerObservation, error) {
	return f(c, a, k, r, id)
}
func ptr(s string) *string { return &s }
func fixture(t *testing.T, kind, action, mode string) (*Engine, model.Job) {
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
	if err != nil {
		t.Fatal(err)
	}
	a := model.Account{ID: "a", Name: "test", Site: "china", RegionIDs: []string{"cn-shanghai"}, Enabled: true, UpdatedAt: time.Now()}
	if _, err = s.CreateAccount(ctx, a, cipher, time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec("INSERT INTO instance VALUES('instance','a','i-test',?,'test','cn-shanghai','running','stopped','now')", kind); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec("INSERT INTO resource_snapshot VALUES('a',?,'instance','cn-shanghai','{}',1,'now')", kind); err != nil {
		t.Fatal(err)
	}
	j, err := s.EnqueuePowerJob(ctx, "instance", action, mode, model.NewID("event_"), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	e := &Engine{Store: s, Box: box, Enabled: true, PollInterval: time.Millisecond, ConfirmationTimeout: time.Second}
	return e, j
}
func readResult(t *testing.T, e *Engine, j model.Job) (model.Job, model.PowerJobResult) {
	t.Helper()
	got, err := e.Store.Job(context.Background(), j.ID)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(got.Result)
	if err != nil {
		t.Fatal(err)
	}
	var result model.PowerJobResult
	if err = json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	return got, result
}

func TestPowerWorkerConfirmsActualStateAndMode(t *testing.T) {
	old := time.Now().UTC().Add(-time.Hour)
	newStart := time.Now().UTC().Truncate(time.Minute)
	cases := []struct {
		name, kind, action, mode, basis, code string
		observations                          []model.PowerObservation
	}{
		{"ECS start", "ecs", "start", "", "target_state", "", []model.PowerObservation{{Status: "stopped"}, {Status: "starting"}, {Status: "running"}}},
		{"SWAS start", "swas", "start", "", "target_state", "", []model.PowerObservation{{Status: "stopped"}, {Status: "running"}}},
		{"ordinary stop", "ecs", "stop", "keep_charging", "target_state", "", []model.PowerObservation{{Status: "running"}, {Status: "stopped", StoppedMode: ptr("keep_charging")}}},
		{"subscription ordinary stop", "ecs", "stop", "keep_charging", "target_state", "", []model.PowerObservation{{Status: "running"}, {Status: "stopped", StoppedMode: ptr("not_applicable")}}},
		{"economical stop", "ecs", "stop", "stop_charging", "target_state", "", []model.PowerObservation{{Status: "running"}, {Status: "stopping"}, {Status: "stopped", StoppedMode: ptr("stop_charging")}}},
		{"fallback ordinary stop", "ecs", "stop", "stop_charging", "", "power_stop_mode_mismatch", []model.PowerObservation{{Status: "running"}, {Status: "stopped", StoppedMode: ptr("keep_charging")}}},
		{"missing stop mode", "ecs", "stop", "stop_charging", "", "power_confirmation_timeout", []model.PowerObservation{{Status: "running"}, {Status: "stopped"}}},
		{"SWAS stop", "swas", "stop", "", "target_state", "", []model.PowerObservation{{Status: "running"}, {Status: "stopped"}}},
		{"ECS reboot timestamp", "ecs", "reboot", "", "start_time_changed", "", []model.PowerObservation{{Status: "running", StartedAt: &old}, {Status: "running", StartedAt: &newStart}}},
		{"ECS reboot transition", "ecs", "reboot", "", "state_transition", "", []model.PowerObservation{{Status: "running"}, {Status: "stopping"}, {Status: "starting"}, {Status: "running"}}},
		{"SWAS reboot transition", "swas", "reboot", "", "state_transition", "", []model.PowerObservation{{Status: "running"}, {Status: "starting"}, {Status: "running"}}},
		{"SWAS unchanged running", "swas", "reboot", "", "", "power_confirmation_timeout", []model.PowerObservation{{Status: "running"}, {Status: "running"}}},
		{"same-minute ECS reboot", "ecs", "reboot", "", "", "power_confirmation_timeout", []model.PowerObservation{{Status: "running", StartedAt: &newStart}, {Status: "running", StartedAt: &newStart}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e, j := fixture(t, tc.kind, tc.action, tc.mode)
			if tc.code == "power_confirmation_timeout" {
				e.ConfirmationTimeout = 30 * time.Millisecond
			}
			writes, reads := 0, 0
			e.Writer = writerFunc(func(ctx context.Context, c aliyun.Credentials, r aliyun.PowerRequest) (aliyun.PowerResult, error) {
				writes++
				if r.Kind != tc.kind || r.InstanceID != "i-test" || r.Region != "cn-shanghai" || r.Action != tc.action || r.StopMode != tc.mode {
					t.Fatal("wrong persisted request", r)
				}
				if tc.kind == "swas" && r.ClientToken != j.ID || tc.kind == "ecs" && r.ClientToken != "" {
					t.Fatal("incorrect token")
				}
				if c.AccessKeySecret != "test-secret-not-real-at-all" {
					t.Fatal("credential load failed")
				}
				p, err := e.Store.PowerOperation(ctx, j.ID)
				if err != nil || p.DispatchStartedAt == nil || p.DispatchOutcome != "dispatching" {
					t.Fatal("write before durable marker", p, err)
				}
				return aliyun.PowerResult{Outcome: "accepted"}, nil
			})
			e.Observer = observerFunc(func(ctx context.Context, c aliyun.Credentials, k, r, id string) (model.PowerObservation, error) {
				if k != tc.kind || r != "cn-shanghai" || id != "i-test" {
					t.Fatal("wrong observation target")
				}
				index := reads
				if index >= len(tc.observations) {
					index = len(tc.observations) - 1
				}
				reads++
				o := tc.observations[index]
				if reads > 1 && tc.basis == "start_time_changed" {
					started := time.Now().UTC().Truncate(time.Minute)
					o.StartedAt = &started
				}
				o.ObservedAt = time.Now().UTC()
				if reads > 1 {
					job, readErr := e.Store.Job(ctx, j.ID)
					if readErr != nil {
						return model.PowerObservation{}, readErr
					}
					if job.Status != "running" {
						t.Fatal("accepted was marked complete before observation")
					}
				}
				return o, nil
			})
			ran, err := e.RunOne(context.Background())
			if err != nil || !ran || writes != 1 {
				t.Fatal(ran, err, writes)
			}
			got, result := readResult(t, e, j)
			wantStatus := "succeeded"
			if tc.code != "" {
				wantStatus = "failed"
				if got.Error == nil || got.Error.Code != tc.code {
					t.Fatal(got)
				}
			}
			if got.Status != wantStatus || result.DispatchOutcome != "accepted" || (tc.code != "power_confirmation_timeout" && result.Observation == nil) || result.Baseline == nil {
				t.Fatal(got, result)
			}
			if tc.basis != "" && (result.ConfirmationBasis == nil || *result.ConfirmationBasis != tc.basis) {
				t.Fatal(result)
			}
			jobs, err := e.Store.Jobs(context.Background(), model.JobFilter{Type: "refresh"}, 0, 20)
			if err != nil || len(jobs) != 1 || jobs[0].Trigger != "system" {
				t.Fatal("post-action discovery not queued", jobs, err)
			}
		})
	}
}

func TestPowerWorkerUncertainCallIsNeverRepeated(t *testing.T) {
	e, j := fixture(t, "ecs", "reboot", "")
	writes, reads := 0, 0
	e.Observer = observerFunc(func(context.Context, aliyun.Credentials, string, string, string) (model.PowerObservation, error) {
		reads++
		return model.PowerObservation{Status: "running", ObservedAt: time.Now().UTC()}, nil
	})
	e.Writer = writerFunc(func(context.Context, aliyun.Credentials, aliyun.PowerRequest) (aliyun.PowerResult, error) {
		writes++
		return aliyun.PowerResult{Outcome: "unknown"}, errors.New("provider-secret-must-not-be-persisted")
	})
	if _, err := e.RunOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if ran, err := e.RunOne(context.Background()); err != nil || ran {
			t.Fatal(ran, err)
		}
	}
	got, result := readResult(t, e, j)
	raw, _ := json.Marshal(got)
	if got.Status != "failed" || got.Error.Code != "power_outcome_unknown" || result.DispatchOutcome != "unknown" || writes != 1 || reads != 1 || strings.Contains(string(raw), "provider-secret") {
		t.Fatal(got, writes, reads)
	}
}

func TestPowerWorkerPreflightAndDisablePreventWrites(t *testing.T) {
	for _, scenario := range []string{"disabled", "bad-credential", "state", "changed-account", "missing"} {
		t.Run(scenario, func(t *testing.T) {
			e, j := fixture(t, "ecs", "start", "")
			reads, writes := 0, 0
			if scenario == "disabled" {
				e.Enabled = false
			}
			if scenario == "bad-credential" {
				e.Box, _ = security.NewBox(bytes.Repeat([]byte{8}, 32))
			}
			e.Writer = writerFunc(func(context.Context, aliyun.Credentials, aliyun.PowerRequest) (aliyun.PowerResult, error) {
				writes++
				return aliyun.PowerResult{}, errors.New("must not call")
			})
			e.Observer = observerFunc(func(context.Context, aliyun.Credentials, string, string, string) (model.PowerObservation, error) {
				reads++
				if scenario == "changed-account" {
					if _, err := e.Store.DB.Exec("UPDATE account_settings SET revision=revision+1"); err != nil {
						t.Fatal(err)
					}
				}
				if scenario == "missing" {
					return model.PowerObservation{}, &aliyun.Error{Code: "instance_not_found"}
				}
				status := "stopped"
				if scenario == "state" {
					status = "running"
				}
				return model.PowerObservation{Status: status, ObservedAt: time.Now().UTC()}, nil
			})
			_, err := e.RunOne(context.Background())
			if err != nil && !errors.Is(err, store.ErrAccountChanged) {
				t.Fatal(err)
			}
			got, result := readResult(t, e, j)
			if writes != 0 || result.DispatchOutcome != "not_sent" {
				t.Fatal("unsafe dispatch", writes, result)
			}
			if scenario == "disabled" {
				if got.Status != "queued" || reads != 0 {
					t.Fatal(got, reads)
				}
			} else if got.Status != "failed" {
				t.Fatal(got)
			}
		})
	}
}

func TestPowerWorkerShutdownDoesNotReplayAcceptedAction(t *testing.T) {
	e, j := fixture(t, "swas", "reboot", "")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	writes := 0
	e.Observer = observerFunc(func(context.Context, aliyun.Credentials, string, string, string) (model.PowerObservation, error) {
		return model.PowerObservation{Status: "running", ObservedAt: time.Now().UTC()}, nil
	})
	e.Writer = writerFunc(func(context.Context, aliyun.Credentials, aliyun.PowerRequest) (aliyun.PowerResult, error) {
		writes++
		cancel()
		return aliyun.PowerResult{Outcome: "accepted"}, nil
	})
	if _, err := e.RunOne(ctx); err != nil {
		t.Fatal(err)
	}
	got, result := readResult(t, e, j)
	if got.Status != "failed" || got.Error.Code != "power_confirmation_interrupted" || result.DispatchOutcome != "accepted" {
		t.Fatal(got, result)
	}
	if ran, err := e.RunOne(context.Background()); ran || err != nil || writes != 1 {
		t.Fatal(ran, err, writes)
	}
}

func TestPowerWorkersDoNotSendSameJobConcurrently(t *testing.T) {
	e, j := fixture(t, "ecs", "start", "")
	entered, release := make(chan struct{}), make(chan struct{})
	reads := 0
	e.Observer = observerFunc(func(context.Context, aliyun.Credentials, string, string, string) (model.PowerObservation, error) {
		reads++
		status := "stopped"
		if reads > 1 {
			status = "running"
		}
		return model.PowerObservation{Status: status, ObservedAt: time.Now().UTC()}, nil
	})
	e.Writer = writerFunc(func(context.Context, aliyun.Credentials, aliyun.PowerRequest) (aliyun.PowerResult, error) {
		close(entered)
		<-release
		return aliyun.PowerResult{Outcome: "accepted"}, nil
	})
	finished := make(chan error, 1)
	go func() { _, err := e.RunOne(context.Background()); finished <- err }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("writer did not start")
	}
	ran, err := e.RunOne(context.Background())
	close(release)
	if ran || err != nil {
		t.Fatal("second worker claimed active request", ran, err)
	}
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not finish")
	}
	got, _ := readResult(t, e, j)
	if got.Status != "succeeded" {
		t.Fatal(got)
	}
}

func TestPowerConfirmationReadFailureAndAccountDisable(t *testing.T) {
	for _, disable := range []bool{false, true} {
		t.Run(map[bool]string{false: "read failure", true: "account disabled"}[disable], func(t *testing.T) {
			e, j := fixture(t, "ecs", "start", "")
			e.ConfirmationTimeout = 30 * time.Millisecond
			reads := 0
			e.Observer = observerFunc(func(context.Context, aliyun.Credentials, string, string, string) (model.PowerObservation, error) {
				reads++
				if reads > 1 {
					return model.PowerObservation{}, errors.New("provider secret")
				}
				return model.PowerObservation{Status: "stopped", ObservedAt: time.Now().UTC()}, nil
			})
			e.Writer = writerFunc(func(context.Context, aliyun.Credentials, aliyun.PowerRequest) (aliyun.PowerResult, error) {
				if disable {
					if _, err := e.Store.UpdateAccount(context.Background(), "a", func(a *model.Account) ([]byte, error) { a.Enabled = false; return nil, nil }, time.Now(), time.Hour); err != nil {
						t.Fatal(err)
					}
				}
				return aliyun.PowerResult{Outcome: "accepted"}, nil
			})
			if _, err := e.RunOne(context.Background()); err != nil {
				t.Fatal(err)
			}
			got, result := readResult(t, e, j)
			code := "power_confirmation_timeout"
			if disable {
				code = "power_confirmation_interrupted"
				if reads != 1 {
					t.Fatal("continued queries after disable")
				}
			}
			if got.Status != "failed" || got.Error.Code != code || result.DispatchOutcome != "accepted" || result.Observation != nil {
				t.Fatal(got, result)
			}
		})
	}
}
