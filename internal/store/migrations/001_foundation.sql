CREATE TABLE app_meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE account (
 id TEXT PRIMARY KEY, name TEXT NOT NULL, site TEXT NOT NULL CHECK(site IN ('china','international')),
 updated_at TEXT NOT NULL
);
CREATE TABLE credential (
 owner_field TEXT PRIMARY KEY, ciphertext BLOB NOT NULL CHECK(length(ciphertext)>=29), updated_at TEXT NOT NULL
);
CREATE TABLE instance (
 id TEXT PRIMARY KEY, account_id TEXT NOT NULL REFERENCES account(id), cloud_id TEXT NOT NULL,
 kind TEXT NOT NULL CHECK(kind IN ('ecs','swas')), name TEXT NOT NULL, region TEXT NOT NULL,
 status TEXT NOT NULL, desired_state TEXT NOT NULL DEFAULT 'stopped', updated_at TEXT NOT NULL,
 UNIQUE(account_id,kind,region,cloud_id)
);
CREATE TABLE billing_subject (
 id TEXT PRIMARY KEY, account_id TEXT NOT NULL REFERENCES account(id), cloud_id TEXT NOT NULL,
 kind TEXT NOT NULL, region TEXT NOT NULL, metering_rule TEXT NOT NULL,
 cdt_eligibility TEXT NOT NULL DEFAULT 'unknown' CHECK(cdt_eligibility IN ('unknown','eligible','ineligible')),
 updated_at TEXT NOT NULL
);
CREATE TABLE subject_member (
 subject_id TEXT NOT NULL REFERENCES billing_subject(id), instance_id TEXT NOT NULL REFERENCES instance(id),
 valid_from TEXT NOT NULL, valid_to TEXT, PRIMARY KEY(subject_id,instance_id,valid_from)
);
CREATE TABLE agent (
 id TEXT PRIMARY KEY, instance_id TEXT NOT NULL UNIQUE REFERENCES instance(id),
 token_hash BLOB NOT NULL UNIQUE CHECK(length(token_hash)=32), revoked_at TEXT,
 version TEXT NOT NULL DEFAULT '', last_received_at TEXT
);
CREATE TABLE traffic_sample (
 agent_id TEXT NOT NULL REFERENCES agent(id), counter_epoch TEXT NOT NULL, sequence INTEGER NOT NULL CHECK(sequence>0),
 sampled_at TEXT NOT NULL, received_at TEXT NOT NULL, uptime_ms INTEGER NOT NULL CHECK(uptime_ms>=0),
 rx_bytes INTEGER NOT NULL CHECK(rx_bytes>=0), tx_bytes INTEGER NOT NULL CHECK(tx_bytes>=0),
 PRIMARY KEY(agent_id,counter_epoch,sequence)
) WITHOUT ROWID;
CREATE INDEX sample_time ON traffic_sample(agent_id,sampled_at);
CREATE INDEX sample_retention ON traffic_sample(received_at);
CREATE TABLE cdt_snapshot (
 id INTEGER PRIMARY KEY, account_id TEXT NOT NULL REFERENCES account(id), region_group TEXT NOT NULL,
 period_start TEXT NOT NULL, covered_until TEXT, fetched_at TEXT NOT NULL,
 usage_bytes INTEGER NOT NULL CHECK(usage_bytes>=0), source TEXT NOT NULL,
 quality TEXT NOT NULL CHECK(quality IN ('verified','unverified','demo'))
);
CREATE TABLE administrator (
 id INTEGER PRIMARY KEY CHECK(id=1), password_hash TEXT NOT NULL, updated_at TEXT NOT NULL
);
CREATE TABLE session (
 token_hash BLOB PRIMARY KEY, expires_at TEXT NOT NULL
);
