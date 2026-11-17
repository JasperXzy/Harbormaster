INSERT INTO alert_rule(key,enabled,params_json,urgent) VALUES('idle_resource_detected',1,'{}',0);
CREATE TABLE idle_alert_target (
 target_id TEXT PRIMARY KEY,
 account_id TEXT NOT NULL REFERENCES account(id),
 region TEXT NOT NULL,
 kind TEXT NOT NULL CHECK(kind IN ('ecs','swas','disk','eip')),
 cloud_id TEXT NOT NULL,
 instance_id TEXT REFERENCES instance(id),
 last_observed_at TEXT NOT NULL,
 UNIQUE(account_id,region,kind,cloud_id)
);
CREATE INDEX idle_alert_scope ON idle_alert_target(account_id,region,kind);
ALTER TABLE notification ADD COLUMN resource_id TEXT REFERENCES idle_alert_target(target_id);
CREATE INDEX notification_resource ON notification(resource_id,rule_key,delivery_status);
