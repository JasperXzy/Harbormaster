CREATE TABLE audit_log (
 seq INTEGER PRIMARY KEY AUTOINCREMENT,
 created_at TEXT NOT NULL,
 actor_kind TEXT NOT NULL CHECK(actor_kind IN ('admin','api_key','cli','telegram','system')),
 actor_id TEXT,
 action TEXT NOT NULL,
 target_type TEXT NOT NULL,
 target_id TEXT NOT NULL,
 account_id TEXT,
 job_id TEXT,
 details_json TEXT NOT NULL CHECK(json_valid(details_json))
);
CREATE INDEX audit_account ON audit_log(account_id,seq DESC);
CREATE INDEX audit_target ON audit_log(target_type,target_id,seq DESC);
CREATE INDEX audit_action ON audit_log(action,seq DESC);
CREATE INDEX audit_actor ON audit_log(actor_kind,seq DESC);
CREATE INDEX audit_created ON audit_log(created_at,seq);

-- These transitions can also happen through cancellation, key revocation,
-- recovery or restore; observe the durable task state in the same transaction.
-- No raw request/result, lease owner, API token or cloud payload is copied.
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
