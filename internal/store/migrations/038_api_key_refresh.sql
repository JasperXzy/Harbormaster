-- Read-only collection may serve several authorized requests. Power jobs keep
-- their existing single-principal ownership and revocation boundary.
CREATE TABLE api_key_refresh_job (
 job_id TEXT NOT NULL REFERENCES job(id),
 key_id TEXT NOT NULL REFERENCES api_key(id),
 joined_at TEXT NOT NULL,
 PRIMARY KEY(job_id,key_id)
);
CREATE INDEX api_key_refresh_owner ON api_key_refresh_job(key_id,job_id);
