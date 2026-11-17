package store

import (
	"context"
	"github.com/JasperXzy/harbormaster/internal/model"
	"testing"
	"time"
)

func reconcileFixture(t *testing.T) (*Store, time.Time) {
	t.Helper()
	s, _, _, base := estimateFixture(t)
	trustQuota(t, s, base)
	if _, err := s.DB.Exec("UPDATE alert_rule SET enabled=1,params_json='{\"consecutive_failures\":3,\"recovery_samples\":3}' WHERE key='cdt_estimation_deviation'"); err != nil {
		t.Fatal(err)
	}
	return s, base
}
func addReconciliationBasis(t *testing.T, s *Store, cutoff, fetched time.Time, usage int64, quality, code string) {
	t.Helper()
	var err error
	if code != "" {
		_, err = s.DB.Exec("INSERT INTO cdt_observation(account_id,credential_version,job_id,attempt,fetched_at,details_json,error_code,source_quality) SELECT account_id,credential_version,job_id,(SELECT max(attempt)+1 FROM cdt_observation),?,'[]',?,'unknown' FROM cdt_observation LIMIT 1", timestamp(fetched), code)
	} else {
		start, end := ReferenceCycle(fetched, time.UTC)
		_, err = s.DB.Exec("INSERT INTO cdt_observation(account_id,credential_version,job_id,attempt,fetched_at,mainland_bytes,overseas_bytes,details_json,covered_until,period_start,period_end,source_quality,source) SELECT account_id,credential_version,job_id,(SELECT max(attempt)+1 FROM cdt_observation),?,?,0,'[]',?,?,?,?,'trusted-fixture' FROM cdt_observation LIMIT 1", timestamp(fetched), usage, timestamp(cutoff), timestamp(start), timestamp(end), quality)
	}
	if err != nil {
		t.Fatal(err)
	}
}
func runReconciliation(t *testing.T, s *Store, now time.Time) model.CDTReconciliation {
	t.Helper()
	ctx := context.Background()
	a, err := s.Account(ctx, "a")
	if err != nil {
		t.Fatal(err)
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	settings, err := readEstimationSettings(ctx, tx)
	if err == nil {
		err = reconcileCDT(ctx, tx, a, "mainland", settings, time.UTC, now)
	}
	if err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	rows, _, err := s.Reconciliations(ctx, "a", "mainland", 0, 200)
	if err != nil || len(rows) == 0 {
		t.Fatal(rows, err)
	}
	return rows[0]
}
func TestReconciliationUsesSameCutoffAndThreeDistinctBadComparisons(t *testing.T) {
	s, base := reconcileFixture(t)
	now := base.Add(121 * time.Second)
	first := runReconciliation(t, s, now)
	if first.Status != "not_comparable" || first.Reason != "previous_trusted_basis_missing" {
		t.Fatal(first)
	}
	for i := 1; i <= 3; i++ {
		cutoff := base.Add(time.Duration(60+i*10) * time.Second)
		addReconciliationBasis(t, s, cutoff, cutoff.Add(time.Second), 1000, "verified", "")
		v := runReconciliation(t, s, now)
		if v.Status != "comparable" || v.Predicted == nil || *v.Predicted != 2000 || v.BadStreak != i || v.AlertActive != (i == 3) {
			t.Fatal(v)
		}
		if i == 1 {
			addReconciliationBasis(t, s, cutoff, cutoff.Add(2*time.Second), 1000, "verified", "")
			duplicate := runReconciliation(t, s, now)
			if duplicate.Status != "duplicate_cutoff" || duplicate.BadStreak != 1 {
				t.Fatal(duplicate)
			}
		}
	}
	notices := alertHistory(t, s)
	if len(notices) != 1 || !notices[0].Urgent {
		t.Fatal(notices)
	}
	// A later fetch of an errored result does not clear an active discrepancy.
	addReconciliationBasis(t, s, time.Time{}, base.Add(95*time.Second), 0, "unknown", "permission_denied")
	v := runReconciliation(t, s, now)
	if v.Status != "not_comparable" || !v.AlertActive || v.BadStreak != 0 || v.HealthyStreak != 0 {
		t.Fatal(v)
	}
	for i := 1; i <= 3; i++ {
		cutoff := base.Add(time.Duration(90+i*10) * time.Second)
		addReconciliationBasis(t, s, cutoff, cutoff.Add(time.Second), int64(1000+i*1000), "verified", "")
		v = runReconciliation(t, s, now)
		if v.Status != "comparable" || v.HealthyStreak != i || v.AlertActive != (i < 3) {
			t.Fatal(v)
		}
	}
	if len(alertHistory(t, s)) != 2 {
		t.Fatal(alertHistory(t, s))
	}
	runReconciliation(t, s, now)
	if len(alertHistory(t, s)) != 2 {
		t.Fatal("replayed already processed observations")
	}
}
func TestReconciliationRefusesBoundaryTopologyAndRegressedCutoffs(t *testing.T) {
	for _, tc := range []string{"boundary", "topology", "unverified", "regressed"} {
		t.Run(tc, func(t *testing.T) {
			s, base := reconcileFixture(t)
			now := base.Add(121 * time.Second)
			runReconciliation(t, s, now)
			cutoff := base.Add(70 * time.Second)
			quality := "verified"
			switch tc {
			case "boundary":
				cutoff = cutoff.Add(time.Second)
			case "topology":
				if _, err := s.DB.Exec("UPDATE metering_topology SET quality='observed'"); err != nil {
					t.Fatal(err)
				}
			case "unverified":
				quality = "inferred"
			case "regressed":
				cutoff = base.Add(50 * time.Second)
			}
			addReconciliationBasis(t, s, cutoff, base.Add(80*time.Second), 1000, quality, "")
			v := runReconciliation(t, s, now)
			if v.Status != "not_comparable" || v.Exceeds != nil || v.RelativeError != nil || v.BadStreak != 0 {
				t.Fatal(v)
			}
		})
	}
}

func TestReconciliationMissingScopeBaselineIsUnknown(t *testing.T) {
	s, base := reconcileFixture(t)
	if _, err := s.DB.Exec("UPDATE cdt_observation SET mainland_bytes=NULL"); err != nil {
		t.Fatal(err)
	}
	addReconciliationBasis(t, s, base.Add(70*time.Second), base.Add(71*time.Second), 1000, "verified", "")
	v := runReconciliation(t, s, base.Add(121*time.Second))
	if v.Status != "not_comparable" || v.Predicted != nil || v.Exceeds != nil {
		t.Fatal(v)
	}
}
