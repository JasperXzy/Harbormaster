CREATE TABLE network_probe_job_next (
 job_id TEXT PRIMARY KEY REFERENCES job(id),
 request_key TEXT NOT NULL UNIQUE,
 point_kind TEXT NOT NULL DEFAULT 'agent' CHECK(point_kind IN ('agent','server')),
 instance_id TEXT REFERENCES instance(id),
 agent_id TEXT REFERENCES agent(id),
 token_hash BLOB,
 credential_version INTEGER,
 request_json TEXT NOT NULL,
 queued_until INTEGER NOT NULL,
 claimed_by TEXT,
 result_json TEXT,
 CHECK((point_kind='server' AND instance_id IS NULL AND agent_id IS NULL AND token_hash IS NULL AND credential_version IS NULL)
 OR (point_kind='agent' AND instance_id IS NOT NULL AND agent_id IS NOT NULL AND token_hash IS NOT NULL AND length(token_hash)=32 AND credential_version IS NOT NULL))
);
INSERT INTO network_probe_job_next(job_id,request_key,instance_id,agent_id,token_hash,credential_version,request_json,queued_until,claimed_by,result_json)
 SELECT job_id,request_key,instance_id,agent_id,token_hash,credential_version,request_json,queued_until,claimed_by,result_json FROM network_probe_job;
DROP TABLE network_probe_job;
ALTER TABLE network_probe_job_next RENAME TO network_probe_job;
CREATE INDEX network_probe_agent ON network_probe_job(agent_id,job_id);
CREATE INDEX network_probe_point ON network_probe_job(point_kind,job_id);
