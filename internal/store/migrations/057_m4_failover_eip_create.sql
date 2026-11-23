CREATE TABLE failover_rule (
 id TEXT PRIMARY KEY,
 revision INTEGER NOT NULL,
 enabled INTEGER NOT NULL CHECK(enabled IN (0,1)),
 dry_run INTEGER NOT NULL CHECK(dry_run IN (0,1)),
 name TEXT NOT NULL,
 primary_instance_id TEXT NOT NULL REFERENCES instance(id),
 standby_instance_id TEXT NOT NULL REFERENCES instance(id),
 last_trigger_key TEXT,
 last_triggered_at TEXT,
 deleted INTEGER NOT NULL DEFAULT 0 CHECK(deleted IN (0,1)),
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL
);

CREATE TABLE failover_power_job (
 job_id TEXT PRIMARY KEY REFERENCES job(id),
 rule_id TEXT NOT NULL REFERENCES failover_rule(id),
 rule_revision INTEGER NOT NULL,
 primary_instance_id TEXT NOT NULL,
 trigger_key TEXT NOT NULL,
 final_notified INTEGER NOT NULL DEFAULT 0 CHECK(final_notified IN (0,1))
);

CREATE TABLE managed_eip (
 allocation_id TEXT PRIMARY KEY,
 account_id TEXT NOT NULL REFERENCES account(id),
 region TEXT NOT NULL,
 ip_address TEXT NOT NULL,
 created_for_instance_id TEXT,
 bandwidth_package_id TEXT,
 created_at TEXT NOT NULL,
 released_at TEXT
);
CREATE INDEX idx_managed_eip_active ON managed_eip(account_id, region) WHERE released_at IS NULL;

CREATE TABLE instance_eip_job (
 job_id TEXT PRIMARY KEY REFERENCES job(id),
 account_id TEXT NOT NULL REFERENCES account(id),
 account_revision INTEGER NOT NULL,
 credential_version INTEGER NOT NULL,
 instance_id TEXT NOT NULL REFERENCES instance(id),
 region TEXT NOT NULL,
 cloud_instance_id TEXT NOT NULL,
 bandwidth_mbps INTEGER NOT NULL,
 charge_type TEXT NOT NULL,
 bandwidth_package_id TEXT,
 old_allocation_id TEXT,
 old_ip_address TEXT,
 new_allocation_id TEXT,
 new_ip_address TEXT,
 step TEXT NOT NULL DEFAULT 'queued',
 event_key TEXT NOT NULL UNIQUE
);

CREATE TABLE instance_create_job (
 job_id TEXT PRIMARY KEY REFERENCES job(id),
 account_id TEXT NOT NULL REFERENCES account(id),
 account_revision INTEGER NOT NULL,
 credential_version INTEGER NOT NULL,
 region TEXT NOT NULL,
 config_json TEXT NOT NULL CHECK(json_valid(config_json)),
 created_vpc_id TEXT,
 created_vswitch_id TEXT,
 created_sg_id TEXT,
 created_cloud_instance_id TEXT,
 step TEXT NOT NULL DEFAULT 'queued',
 event_key TEXT NOT NULL UNIQUE
);
