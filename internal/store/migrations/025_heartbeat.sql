-- Preserve every power child relation while rebuilding the job type constraint.
CREATE TEMP TABLE heartbeat_migration_api_key_power AS SELECT * FROM api_key_power_job;
CREATE TEMP TABLE heartbeat_migration_power_request AS SELECT * FROM instance_power_request;
CREATE TEMP TABLE heartbeat_migration_intent AS SELECT * FROM instance_intent;
CREATE TEMP TABLE heartbeat_migration_power AS SELECT * FROM instance_power_job;
DELETE FROM api_key_power_job;
DELETE FROM instance_power_request;
DELETE FROM instance_intent;
DELETE FROM instance_power_job;
-- Rebuild job's type constraint without disabling foreign keys. Temporarily
-- detach its four existing inbound relations in this same transaction, then
-- restore their original rows and sequence values. No delivery triggers watch
-- notification.job_id, so detaching it emits no synthetic delivery event.
CREATE TEMP TABLE power_migration_job_change AS SELECT * FROM job_change;
CREATE TEMP TABLE power_migration_job_order AS SELECT * FROM job_order;
CREATE TEMP TABLE power_migration_cdt AS SELECT * FROM cdt_observation;
CREATE TEMP TABLE power_migration_notification AS SELECT seq,job_id FROM notification WHERE job_id IS NOT NULL;
DELETE FROM job_change;
DELETE FROM job_order;
DELETE FROM cdt_observation;
UPDATE notification SET job_id=NULL WHERE job_id IS NOT NULL;
CREATE TABLE job_new (
 id TEXT PRIMARY KEY,
 type TEXT NOT NULL CHECK(type IN ('refresh','permission_check','telegram_test','instance_power','heartbeat_test')),
 scope TEXT NOT NULL,
 account_ids_json TEXT NOT NULL,
 status TEXT NOT NULL CHECK(status IN ('queued','running','succeeded','failed')),
 created_at TEXT NOT NULL, started_at TEXT, finished_at TEXT,
 error_code TEXT, result_json TEXT,
 attempts INTEGER NOT NULL DEFAULT 0,
 available_at INTEGER NOT NULL DEFAULT 0,
 lease_owner TEXT, lease_until INTEGER NOT NULL DEFAULT 0,
 trigger_source TEXT NOT NULL DEFAULT 'unknown'
 CHECK(trigger_source IN ('panel','cli','api','telegram','schedule','system','unknown')),
 instance_id TEXT REFERENCES instance(id)
);
INSERT INTO job_new SELECT * FROM job;
DROP TABLE job;
ALTER TABLE job_new RENAME TO job;
INSERT INTO job_change SELECT * FROM power_migration_job_change;
INSERT INTO job_order SELECT * FROM power_migration_job_order;
INSERT INTO cdt_observation SELECT * FROM power_migration_cdt;
UPDATE notification SET job_id=(SELECT job_id FROM power_migration_notification b WHERE b.seq=notification.seq)
 WHERE seq IN (SELECT seq FROM power_migration_notification);
DROP TABLE power_migration_job_change;
DROP TABLE power_migration_job_order;
DROP TABLE power_migration_cdt;
DROP TABLE power_migration_notification;
CREATE UNIQUE INDEX job_active_scope ON job(type,scope) WHERE status IN ('queued','running');
CREATE INDEX job_claimable ON job(status,available_at,lease_until);
CREATE TRIGGER job_created AFTER INSERT ON job BEGIN INSERT INTO job_change(job_id) VALUES(new.id); END;
CREATE TRIGGER job_updated AFTER UPDATE OF status,result_json,error_code ON job
 WHEN old.status<>new.status OR old.result_json IS NOT new.result_json OR old.error_code IS NOT new.error_code
 BEGIN INSERT INTO job_change(job_id) VALUES(new.id); END;
CREATE TRIGGER job_order_created AFTER INSERT ON job
 BEGIN INSERT INTO job_order(job_id) VALUES(new.id); END;
INSERT INTO instance_power_job SELECT * FROM heartbeat_migration_power;
INSERT INTO instance_intent SELECT * FROM heartbeat_migration_intent;
INSERT INTO instance_power_request SELECT * FROM heartbeat_migration_power_request;
INSERT INTO api_key_power_job SELECT * FROM heartbeat_migration_api_key_power;
DROP TABLE heartbeat_migration_power;
DROP TABLE heartbeat_migration_intent;
DROP TABLE heartbeat_migration_power_request;
DROP TABLE heartbeat_migration_api_key_power;
CREATE TABLE heartbeat_settings (
 id INTEGER PRIMARY KEY CHECK(id=1),
 enabled INTEGER NOT NULL DEFAULT 0 CHECK(enabled IN (0,1)),
 interval_seconds INTEGER NOT NULL DEFAULT 60 CHECK(interval_seconds BETWEEN 15 AND 86400),
 revision INTEGER NOT NULL DEFAULT 0,
 updated_at TEXT,
 last_attempt_at TEXT,
 last_success_at TEXT,
 last_error_code TEXT,
 next_due_at INTEGER NOT NULL DEFAULT 0,
 lease_owner TEXT,
 lease_until INTEGER NOT NULL DEFAULT 0
);
INSERT INTO heartbeat_settings(id) VALUES(1);
CREATE TABLE heartbeat_test (
 job_id TEXT PRIMARY KEY REFERENCES job(id),
 revision INTEGER NOT NULL
);
