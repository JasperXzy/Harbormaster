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
 type TEXT NOT NULL CHECK(type IN ('refresh','permission_check','telegram_test','instance_power')),
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
CREATE TABLE instance_power_job (
 job_id TEXT PRIMARY KEY REFERENCES job(id),
 account_revision INTEGER NOT NULL,
 action TEXT NOT NULL CHECK(action IN ('start','stop','reboot')),
 stop_mode TEXT CHECK(stop_mode IN ('keep_charging','stop_charging')),
 dispatch_started_at TEXT,
 dispatch_outcome TEXT NOT NULL DEFAULT 'not_sent'
 CHECK(dispatch_outcome IN ('not_sent','dispatching','accepted','rejected','unknown'))
);
CREATE TABLE instance_power_request (
 event_key TEXT PRIMARY KEY,
 job_id TEXT NOT NULL REFERENCES instance_power_job(job_id)
);
