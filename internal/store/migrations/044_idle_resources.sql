CREATE TABLE idle_resource_source (
 account_id TEXT NOT NULL REFERENCES account(id),
 credential_version INTEGER NOT NULL CHECK(credential_version>=1),
 region TEXT NOT NULL,
 kind TEXT NOT NULL CHECK(kind IN ('ecs','swas','disk','eip')),
 fetched_at TEXT,
 last_attempt_at TEXT NOT NULL,
 error_code TEXT,
 scanned_count INTEGER,
 unclassified_count INTEGER,
 candidate_count INTEGER,
 PRIMARY KEY(account_id,credential_version,region,kind),
 CHECK((fetched_at IS NULL AND scanned_count IS NULL AND unclassified_count IS NULL AND candidate_count IS NULL) OR
       (fetched_at IS NOT NULL AND scanned_count IS NOT NULL AND unclassified_count IS NOT NULL AND candidate_count IS NOT NULL
        AND scanned_count>=0 AND unclassified_count>=0 AND candidate_count>=0
        AND unclassified_count<=scanned_count AND candidate_count<=scanned_count))
);
CREATE TABLE idle_resource_candidate (
 account_id TEXT NOT NULL,
 credential_version INTEGER NOT NULL,
 region TEXT NOT NULL,
 kind TEXT NOT NULL,
 id TEXT NOT NULL,
 cloud_id TEXT NOT NULL,
 payload_json TEXT NOT NULL CHECK(json_valid(payload_json)),
 retained INTEGER NOT NULL DEFAULT 0 CHECK(retained IN (0,1)),
 PRIMARY KEY(account_id,credential_version,region,kind,cloud_id),
 FOREIGN KEY(account_id,credential_version,region,kind)
 REFERENCES idle_resource_source(account_id,credential_version,region,kind)
);
CREATE INDEX idle_resource_page ON idle_resource_candidate(id,account_id,credential_version);
