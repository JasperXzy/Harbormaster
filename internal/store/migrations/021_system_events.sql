ALTER TABLE account_settings ADD COLUMN next_system_event_at INTEGER NOT NULL DEFAULT 0;
CREATE TABLE system_event_source (
 account_id TEXT NOT NULL REFERENCES account(id),
 region_id TEXT NOT NULL,
 source TEXT NOT NULL CHECK(source IN ('ecs_history','cms_spot')),
 revision INTEGER NOT NULL,
 checked_at TEXT NOT NULL,
 last_success_at TEXT,
 error_code TEXT,
 PRIMARY KEY(account_id,region_id,source)
);
CREATE TABLE system_event (
 sequence INTEGER PRIMARY KEY AUTOINCREMENT,
 id TEXT NOT NULL UNIQUE,
 account_id TEXT NOT NULL REFERENCES account(id),
 region_id TEXT NOT NULL,
 source TEXT NOT NULL CHECK(source IN ('ecs_history','cms_spot')),
 cloud_event_id TEXT NOT NULL,
 cloud_instance_id TEXT NOT NULL,
 category TEXT NOT NULL CHECK(category IN ('maintenance','spot_interruption','other')),
 payload_json TEXT NOT NULL,
 first_seen_at TEXT NOT NULL,
 observed_at TEXT NOT NULL,
 seen_in_latest_scan INTEGER NOT NULL CHECK(seen_in_latest_scan IN (0,1)),
 UNIQUE(account_id,region_id,source,cloud_event_id)
);
CREATE INDEX system_event_account ON system_event(account_id,sequence);
CREATE INDEX system_event_instance ON system_event(account_id,region_id,cloud_instance_id,sequence);
