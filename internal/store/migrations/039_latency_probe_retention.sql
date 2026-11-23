ALTER TABLE network_probe_job ADD COLUMN result_digest BLOB CHECK(result_digest IS NULL OR length(result_digest)=32);
ALTER TABLE network_probe_job ADD COLUMN result_pruned_at TEXT;

-- A small time-indexed worklist avoids rescanning already pruned task metadata.
CREATE TABLE latency_probe_retention (
 job_id TEXT PRIMARY KEY REFERENCES network_probe_job(job_id),
 completed_at TEXT NOT NULL
);
CREATE INDEX latency_probe_retention_due ON latency_probe_retention(completed_at,job_id);
CREATE INDEX probe_plan_run_retention ON probe_plan_run(created_at,seq);

INSERT INTO latency_probe_retention(job_id,completed_at)
 SELECT p.job_id,j.finished_at FROM network_probe_job p JOIN job j ON j.id=p.job_id
 WHERE p.action='probe' AND j.status IN ('succeeded','failed') AND j.finished_at IS NOT NULL;

CREATE TRIGGER latency_probe_finished AFTER UPDATE OF status,finished_at ON job
 WHEN new.type='network_probe' AND new.status IN ('succeeded','failed') AND new.finished_at IS NOT NULL
 BEGIN
 INSERT INTO latency_probe_retention(job_id,completed_at)
 SELECT p.job_id,new.finished_at FROM network_probe_job p
 WHERE p.job_id=new.id AND p.action='probe' AND p.result_pruned_at IS NULL
 ON CONFLICT(job_id) DO UPDATE SET completed_at=excluded.completed_at;
 END;
