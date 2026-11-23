INSERT INTO alert_rule VALUES
 ('network_latency',1,'{"probe_thresholds":[],"duration_seconds":600,"recovery_samples":3}',0),
 ('network_loss',1,'{"probe_thresholds":[],"recovery_samples":3}',0),
 ('network_unreachable',1,'{"probe_thresholds":[],"consecutive_failures":3,"recovery_samples":3}',0);
ALTER TABLE notification ADD COLUMN probe_plan_id TEXT REFERENCES probe_plan(id);
CREATE INDEX notification_probe_plan ON notification(probe_plan_id,rule_key);
CREATE TABLE probe_alert_progress (
 rule_key TEXT NOT NULL REFERENCES alert_rule(key),
 plan_id TEXT NOT NULL REFERENCES probe_plan(id),
 last_seq INTEGER NOT NULL DEFAULT 0,
 plan_revision INTEGER NOT NULL DEFAULT 0,
 target_revision INTEGER NOT NULL DEFAULT 0,
 bad_since TEXT,
 bad_count INTEGER NOT NULL DEFAULT 0,
 good_count INTEGER NOT NULL DEFAULT 0,
 last_created_at TEXT,
 last_sample_at TEXT,
 last_address TEXT NOT NULL DEFAULT '',
 updated_at TEXT NOT NULL,
 PRIMARY KEY(rule_key,plan_id)
);
