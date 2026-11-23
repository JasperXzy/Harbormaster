-- All existing inbound job references use NO ACTION. Keep their rows and
-- sequence values in place while replacing the CHECK-constrained parent.
-- foreign_keys remains ON. Validate every relation before resetting the stale
-- deferred DROP-table counter and returning to immediate enforcement.
PRAGMA defer_foreign_keys=ON;
CREATE TEMP TABLE release_migration_jobs AS SELECT * FROM job;
DROP TABLE job;
CREATE TABLE job (
 id TEXT PRIMARY KEY,
 type TEXT NOT NULL CHECK(type IN ('refresh','permission_check','telegram_test','instance_power','instance_release','heartbeat_test','network_probe')),
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
INSERT INTO job SELECT * FROM release_migration_jobs;
DROP TABLE release_migration_jobs;
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

CREATE TABLE instance_release_job (
 job_id TEXT PRIMARY KEY REFERENCES job(id),
 account_id TEXT NOT NULL REFERENCES account(id),
 account_revision INTEGER NOT NULL,
 credential_version INTEGER NOT NULL,
 region TEXT NOT NULL,
 cloud_instance_id TEXT NOT NULL,
 confirmed_name TEXT NOT NULL,
 preview_token TEXT NOT NULL CHECK(length(preview_token)=64),
 preview_fetched_at TEXT NOT NULL,
 impact_json TEXT NOT NULL CHECK(json_valid(impact_json)),
 dispatch_started_at TEXT,
 dispatch_outcome TEXT NOT NULL DEFAULT 'not_sent'
 CHECK(dispatch_outcome IN ('not_sent','dispatching','accepted','rejected','unknown')),
 observation_json TEXT CHECK(observation_json IS NULL OR json_valid(observation_json)),
 confirmation_basis TEXT CHECK(confirmation_basis IS NULL OR confirmation_basis='not_found_after_accept')
);
CREATE TABLE instance_release_request (
 event_key TEXT PRIMARY KEY,
 job_id TEXT NOT NULL REFERENCES job(id)
);
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
CREATE TEMP TABLE release_migration_fk_assert(n INTEGER NOT NULL CHECK(n=0));
INSERT INTO release_migration_fk_assert SELECT count(*) FROM pragma_foreign_key_check;
DROP TABLE release_migration_fk_assert;
PRAGMA defer_foreign_keys=OFF;
