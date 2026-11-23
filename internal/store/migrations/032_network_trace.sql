ALTER TABLE network_probe_job ADD COLUMN action TEXT NOT NULL DEFAULT 'probe' CHECK(action IN ('probe','trace'));
CREATE INDEX network_probe_action ON network_probe_job(agent_id,action,job_id);
