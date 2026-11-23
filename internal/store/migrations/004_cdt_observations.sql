ALTER TABLE account_settings ADD COLUMN next_cdt_at INTEGER NOT NULL DEFAULT 0;
ALTER TABLE account_settings ADD COLUMN credential_version INTEGER NOT NULL DEFAULT 1;
CREATE TABLE cdt_observation (
 id INTEGER PRIMARY KEY,
 account_id TEXT NOT NULL REFERENCES account(id),
 credential_version INTEGER NOT NULL,
 job_id TEXT NOT NULL REFERENCES job(id), attempt INTEGER NOT NULL,
 fetched_at TEXT NOT NULL,
 mainland_bytes INTEGER CHECK(mainland_bytes IS NULL OR mainland_bytes>=0),
 overseas_bytes INTEGER CHECK(overseas_bytes IS NULL OR overseas_bytes>=0),
 details_json TEXT NOT NULL,
 error_code TEXT,
 UNIQUE(account_id,job_id,attempt)
);
CREATE INDEX cdt_observation_time ON cdt_observation(account_id,fetched_at DESC,id DESC);
