CREATE UNIQUE INDEX account_name_unique ON account(name);
CREATE TABLE account_settings (
 account_id TEXT PRIMARY KEY REFERENCES account(id),
 config_json TEXT NOT NULL,
 enabled INTEGER NOT NULL CHECK(enabled IN (0,1)),
 revision INTEGER NOT NULL DEFAULT 1,
 lease_owner TEXT, lease_until INTEGER NOT NULL DEFAULT 0,
 next_scan_at INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE job (
 id TEXT PRIMARY KEY,
 type TEXT NOT NULL CHECK(type IN ('refresh','permission_check','telegram_test')),
 scope TEXT NOT NULL,
 account_ids_json TEXT NOT NULL,
 status TEXT NOT NULL CHECK(status IN ('queued','running','succeeded','failed')),
 created_at TEXT NOT NULL, started_at TEXT, finished_at TEXT,
 error_code TEXT, result_json TEXT,
 attempts INTEGER NOT NULL DEFAULT 0,
 available_at INTEGER NOT NULL DEFAULT 0,
 lease_owner TEXT, lease_until INTEGER NOT NULL DEFAULT 0
);
CREATE UNIQUE INDEX job_active_scope ON job(type,scope) WHERE status IN ('queued','running');
CREATE INDEX job_claimable ON job(status,available_at,lease_until);
CREATE TABLE job_change (seq INTEGER PRIMARY KEY AUTOINCREMENT, job_id TEXT NOT NULL REFERENCES job(id));
CREATE TRIGGER job_created AFTER INSERT ON job BEGIN INSERT INTO job_change(job_id) VALUES(new.id); END;
CREATE TRIGGER job_updated AFTER UPDATE OF status,result_json,error_code ON job
 WHEN old.status<>new.status OR old.result_json IS NOT new.result_json OR old.error_code IS NOT new.error_code
 BEGIN INSERT INTO job_change(job_id) VALUES(new.id); END;
CREATE TABLE resource_snapshot (
 account_id TEXT NOT NULL REFERENCES account(id), kind TEXT NOT NULL,
 id TEXT NOT NULL, region TEXT NOT NULL, payload_json TEXT NOT NULL,
 present INTEGER NOT NULL DEFAULT 1 CHECK(present IN (0,1)), updated_at TEXT NOT NULL,
 PRIMARY KEY(account_id,kind,id)
);
CREATE INDEX resource_snapshot_list ON resource_snapshot(kind,present,account_id,id);
