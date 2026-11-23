CREATE TABLE release_inventory (
 account_id TEXT NOT NULL REFERENCES account(id),
 credential_version INTEGER NOT NULL CHECK(credential_version>=1),
 region TEXT NOT NULL,
 fetched_at TEXT,
 last_attempt_at TEXT NOT NULL,
 error_code TEXT,
 payload_json TEXT,
 PRIMARY KEY(account_id,credential_version,region),
 CHECK((fetched_at IS NULL AND payload_json IS NULL) OR
       (fetched_at IS NOT NULL AND payload_json IS NOT NULL))
);
