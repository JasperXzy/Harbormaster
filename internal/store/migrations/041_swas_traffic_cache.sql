CREATE TABLE swas_traffic_cache (
 instance_id TEXT NOT NULL REFERENCES instance(id),
 credential_version INTEGER NOT NULL CHECK(credential_version>=1),
 plan_id TEXT NOT NULL,
 fetched_at TEXT,
 last_attempt_at TEXT NOT NULL,
 error_code TEXT,
 entry_present INTEGER CHECK(entry_present IN (0,1)),
 usage_json TEXT,
 PRIMARY KEY(instance_id,credential_version,plan_id),
 CHECK(
  (entry_present IS NULL AND fetched_at IS NULL AND usage_json IS NULL) OR
  (entry_present IS 0 AND fetched_at IS NOT NULL AND usage_json IS NULL) OR
  (entry_present IS 1 AND fetched_at IS NOT NULL AND usage_json IS NOT NULL AND json_valid(usage_json))
 )
);
