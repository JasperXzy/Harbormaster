PRAGMA defer_foreign_keys=ON;
CREATE INDEX IF NOT EXISTS idx_job_change_job_id ON job_change(job_id);
CREATE INDEX IF NOT EXISTS idx_cdt_observation_job_id ON cdt_observation(job_id);
CREATE INDEX IF NOT EXISTS idx_notification_job_id ON notification(job_id);
CREATE INDEX IF NOT EXISTS idx_instance_intent_job_id ON instance_intent(job_id);
CREATE INDEX IF NOT EXISTS idx_probe_plan_run_fk_job_id ON probe_plan_run(job_id);
CREATE INDEX IF NOT EXISTS idx_instance_release_request_job_id ON instance_release_request(job_id);
CREATE TEMP TABLE m4_migration_jobs AS SELECT * FROM job;
DROP TABLE job;
CREATE TABLE job (
 id TEXT PRIMARY KEY,
 type TEXT NOT NULL CHECK(type IN ('refresh','permission_check','telegram_test','email_test','webhook_test','instance_power','instance_release','instance_eip_rotate','instance_create','heartbeat_test','network_probe')),
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
INSERT INTO job SELECT * FROM m4_migration_jobs;
DROP TABLE m4_migration_jobs;
CREATE UNIQUE INDEX job_active_scope ON job(type,scope) WHERE status IN ('queued','running');
CREATE INDEX job_claimable ON job(status,available_at,lease_until);
CREATE TRIGGER job_created AFTER INSERT ON job BEGIN INSERT INTO job_change(job_id) VALUES(new.id); END;
CREATE TRIGGER job_updated AFTER UPDATE OF status,result_json,error_code ON job
 WHEN old.status<>new.status OR old.result_json IS NOT new.result_json OR old.error_code IS NOT new.error_code
 BEGIN INSERT INTO job_change(job_id) VALUES(new.id); END;
CREATE TRIGGER job_order_created AFTER INSERT ON job
 BEGIN INSERT INTO job_order(job_id) VALUES(new.id); END;
CREATE TRIGGER audit_power_started AFTER UPDATE OF status ON job
 WHEN new.type='instance_power' AND old.status<>new.status AND new.status='running'
 BEGIN
 INSERT INTO audit_log(created_at,actor_kind,action,target_type,target_id,account_id,job_id,details_json)
 SELECT coalesce(new.started_at,strftime('%Y-%m-%dT%H:%M:%f000000Z','now')),'system','instance_power_started','instance',new.instance_id,
 (SELECT account_id FROM instance WHERE id=new.instance_id),new.id,
 json_object('changed_fields',json('["status"]'),'credentials_changed',json('false'),'operation',p.action,'stop_mode',p.stop_mode,'status',new.status,'dispatch_outcome',p.dispatch_outcome)
 FROM instance_power_job p WHERE p.job_id=new.id;
 END;
CREATE TRIGGER audit_power_finished AFTER UPDATE OF status ON job
 WHEN new.type='instance_power' AND old.status<>new.status AND new.status IN ('succeeded','failed')
 BEGIN
 INSERT INTO audit_log(created_at,actor_kind,action,target_type,target_id,account_id,job_id,details_json)
 SELECT coalesce(new.finished_at,strftime('%Y-%m-%dT%H:%M:%f000000Z','now')),'system','instance_power_finished','instance',new.instance_id,
 (SELECT account_id FROM instance WHERE id=new.instance_id),new.id,
 json_object('changed_fields',json('["status"]'),'credentials_changed',json('false'),'operation',p.action,'stop_mode',p.stop_mode,'status',new.status,'dispatch_outcome',p.dispatch_outcome,'error_code',new.error_code,'confirmation_basis',p.confirmation_basis)
 FROM instance_power_job p WHERE p.job_id=new.id;
 END;
CREATE TRIGGER latency_probe_finished AFTER UPDATE OF status,finished_at ON job
 WHEN new.type='network_probe' AND new.status IN ('succeeded','failed') AND new.finished_at IS NOT NULL
 BEGIN
 INSERT INTO latency_probe_retention(job_id,completed_at)
 SELECT p.job_id,new.finished_at FROM network_probe_job p
 WHERE p.job_id=new.id AND p.action='probe' AND p.result_pruned_at IS NULL
 ON CONFLICT(job_id) DO UPDATE SET completed_at=excluded.completed_at;
 END;
CREATE TRIGGER audit_release_started AFTER UPDATE OF status ON job
 WHEN new.type='instance_release' AND old.status<>new.status AND new.status='running'
 BEGIN
 INSERT INTO audit_log(created_at,actor_kind,action,target_type,target_id,account_id,job_id,details_json)
 SELECT coalesce(new.started_at,strftime('%Y-%m-%dT%H:%M:%f000000Z','now')),'system','instance_release_started','instance',new.instance_id,
 p.account_id,new.id,json_object('changed_fields',json('["status"]'),'credentials_changed',json('false'),'operation','release','status',new.status,'dispatch_outcome',p.dispatch_outcome)
 FROM instance_release_job p WHERE p.job_id=new.id;
 END;
CREATE TRIGGER audit_release_finished AFTER UPDATE OF status ON job
 WHEN new.type='instance_release' AND old.status<>new.status AND new.status IN ('succeeded','failed')
 BEGIN
 INSERT INTO audit_log(created_at,actor_kind,action,target_type,target_id,account_id,job_id,details_json)
 SELECT coalesce(new.finished_at,strftime('%Y-%m-%dT%H:%M:%f000000Z','now')),'system','instance_release_finished','instance',new.instance_id,
 p.account_id,new.id,json_object('changed_fields',json('["status"]'),'credentials_changed',json('false'),'operation','release','status',new.status,'dispatch_outcome',p.dispatch_outcome,'error_code',new.error_code,'confirmation_basis',p.confirmation_basis)
 FROM instance_release_job p WHERE p.job_id=new.id;
 END;

CREATE TABLE email_channel (
 id INTEGER PRIMARY KEY CHECK(id=1),
 enabled INTEGER NOT NULL DEFAULT 1 CHECK(enabled IN (0,1)),
 revision INTEGER NOT NULL DEFAULT 1,
 ciphertext BLOB NOT NULL CHECK(length(ciphertext)>=29),
 updated_at TEXT NOT NULL,
 last_success_at TEXT,
 last_error_code TEXT
);

CREATE TABLE webhook_channel (
 id INTEGER PRIMARY KEY CHECK(id=1),
 enabled INTEGER NOT NULL DEFAULT 1 CHECK(enabled IN (0,1)),
 revision INTEGER NOT NULL DEFAULT 1,
 ciphertext BLOB NOT NULL CHECK(length(ciphertext)>=29),
 updated_at TEXT NOT NULL,
 last_success_at TEXT,
 last_error_code TEXT
);

CREATE TABLE notification_channel_delivery (
 notification_id TEXT NOT NULL REFERENCES notification(id) ON DELETE CASCADE,
 channel TEXT NOT NULL CHECK(channel IN ('email','webhook')),
 channel_revision INTEGER NOT NULL,
 job_id TEXT REFERENCES job(id),
 delivery_status TEXT NOT NULL CHECK(delivery_status IN ('pending','sent','failed','not_sent')),
 attempts INTEGER NOT NULL DEFAULT 0,
 available_at INTEGER NOT NULL DEFAULT 0,
 lease_owner TEXT,
 lease_until INTEGER NOT NULL DEFAULT 0,
 last_error_code TEXT,
 delivered_at TEXT,
 PRIMARY KEY(notification_id, channel)
);
CREATE INDEX idx_notif_chan_claimable ON notification_channel_delivery(delivery_status, available_at, lease_until);
CREATE INDEX idx_notif_chan_job_id ON notification_channel_delivery(job_id);

CREATE TABLE daily_report_settings (
 id INTEGER PRIMARY KEY CHECK(id=1),
 enabled INTEGER NOT NULL DEFAULT 0 CHECK(enabled IN (0,1)),
 time_of_day TEXT NOT NULL DEFAULT '09:00',
 revision INTEGER NOT NULL DEFAULT 1,
 last_sent_date TEXT,
 updated_at TEXT
);
INSERT OR IGNORE INTO daily_report_settings(id,enabled,time_of_day,revision) VALUES(1,0,'09:00',1);

CREATE TEMP TABLE m4_migration_fk_assert(n INTEGER NOT NULL CHECK(n=0));
INSERT INTO m4_migration_fk_assert SELECT count(*) FROM pragma_foreign_key_check;
DROP TABLE m4_migration_fk_assert;
PRAGMA defer_foreign_keys=OFF;
