CREATE TABLE api_key_power_job (
 job_id TEXT PRIMARY KEY REFERENCES instance_power_job(job_id),
 key_id TEXT NOT NULL REFERENCES api_key(id)
);
CREATE INDEX api_key_power_owner ON api_key_power_job(key_id,job_id);
CREATE TABLE instance_power_request_new (
 principal_id TEXT NOT NULL DEFAULT 'panel',
 event_key TEXT NOT NULL,
 job_id TEXT NOT NULL REFERENCES instance_power_job(job_id),
 PRIMARY KEY(principal_id,event_key)
);
INSERT INTO instance_power_request_new(event_key,job_id) SELECT event_key,job_id FROM instance_power_request;
DROP TABLE instance_power_request;
ALTER TABLE instance_power_request_new RENAME TO instance_power_request;
