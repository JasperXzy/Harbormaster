package collect

import (
	"context"
	"github.com/JasperXzy/harbormaster/internal/model"
	"testing"
	"time"
)

func TestSchedulerProgressContinuesDespiteCloudAccountFailure(t *testing.T) {
	e, _ := testEngine(t)
	called := make(chan struct{}, 1)
	e.Scanner = scanFunc(func(context.Context, model.Account, model.Credential, bool, func() error) model.ScanResult {
		select {
		case called <- struct{}{}:
		default:
		}
		return model.ScanResult{Failure: "aliyun_unreachable"}
	})
	if _, _, err := e.CreateAccount(context.Background(), testCreate("failure")); err != nil {
		t.Fatal(err)
	}
	if !e.SchedulerProgress().IsZero() {
		t.Fatal("scheduler progress before Run")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); e.Run(ctx) }()
	select {
	case <-called:
	case <-time.After(3 * time.Second):
		t.Fatal("account worker did not run")
	}
	deadline := time.Now().Add(3 * time.Second)
	for e.SchedulerProgress().IsZero() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	progress := e.SchedulerProgress()
	if progress.IsZero() || time.Since(progress) > 30*time.Second {
		t.Fatal("cloud error suppressed scheduler evidence")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("workers did not join")
	}
}
