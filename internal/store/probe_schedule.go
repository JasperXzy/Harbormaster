package store

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/JasperXzy/harbormaster/internal/model"
)

// ScheduleProbePlans takes one current observation opportunity per overdue
// plan. It never loops over missed historical intervals.
func (s *Store) ScheduleProbePlans(ctx context.Context, now time.Time) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = expireNetworkProbes(ctx, tx, now); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, "SELECT id,next_at FROM probe_plan WHERE enabled=1 AND deleted=0 AND next_at<=? ORDER BY next_at,id LIMIT 100", now.Unix())
	if err != nil {
		return err
	}
	type due struct {
		id string
		at int64
	}
	items := []due{}
	for rows.Next() {
		var d due
		if err = rows.Scan(&d.id, &d.at); err != nil {
			rows.Close()
			return err
		}
		items = append(items, d)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	ctx = model.WithJobTrigger(ctx, "schedule")
	for _, d := range items {
		plan, err := readProbePlan(ctx, tx, d.id)
		if err != nil {
			return err
		}
		target, err := readTarget(ctx, tx, plan.TargetID)
		if err != nil {
			return err
		}
		reason := ""
		var jobID any
		var active int
		if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM probe_plan_run r JOIN job j ON j.id=r.job_id WHERE r.plan_id=? AND j.status IN ('queued','running')", plan.ID).Scan(&active); err != nil {
			return err
		}
		if active > 0 {
			reason = "previous_run_pending"
		}
		pointID := "server"
		if plan.InstanceID != nil {
			pointID = *plan.InstanceID
		}
		if reason == "" {
			point, err := probePoint(ctx, tx, pointID, now)
			if errors.Is(err, ErrNotFound) {
				reason = "point_unavailable"
			} else if err != nil {
				return err
			}
			if err == nil && !point.Available {
				reason = *point.UnavailableReason
			}
		}
		host := ""
		if reason == "" {
			host, reason, err = resolveProbeTarget(ctx, tx, target, plan.Family, now)
			if err != nil {
				return err
			}
		}
		port := 0
		if plan.Method == "tcp" {
			if target.TCPPort == nil {
				if reason == "" {
					reason = "target_tcp_port_required"
				}
			} else {
				port = *target.TCPPort
			}
		}
		request := plan.Request(host, port)
		if reason == "" && request.Validate() != nil {
			reason = "target_address_family_or_format"
		}
		if reason == "" {
			key := "scheduled_" + plan.ID + "_" + strconv.FormatInt(plan.Revision, 10) + "_" + strconv.FormatInt(d.at, 10)
			var job model.Job
			if plan.PointKind == "server" {
				job, err = enqueueServerProbe(ctx, tx, key, request, now)
			} else {
				job, err = enqueueAgentProbe(ctx, tx, *plan.InstanceID, key, request, now)
			}
			if errors.Is(err, ErrProbeBusy) {
				reason = "probe_queue_full"
			} else if errors.Is(err, ErrAccountDisabled) {
				reason = "point_unavailable"
			} else if err != nil {
				return err
			} else {
				jobID = job.ID
			}
		}
		var skip any
		if reason != "" {
			skip = reason
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO probe_plan_run(plan_id,plan_revision,target_id,target_revision,point_kind,instance_id,created_at,job_id,skip_reason) VALUES(?,?,?,?,?,?,?,?,?)", plan.ID, plan.Revision, target.ID, target.Revision, plan.PointKind, plan.InstanceID, timestamp(now), jobID, skip); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "UPDATE probe_plan SET next_at=? WHERE id=?", now.Add(time.Duration(plan.FrequencySeconds)*time.Second).Unix(), plan.ID); err != nil {
			return err
		}
	}
	return tx.Commit()
}
