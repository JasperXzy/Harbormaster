CREATE TABLE quota_rule (
 id TEXT PRIMARY KEY, revision INTEGER NOT NULL, enabled INTEGER NOT NULL, deleted INTEGER NOT NULL DEFAULT 0,
 account_id TEXT NOT NULL REFERENCES account(id), config_json TEXT NOT NULL,
 created_at TEXT NOT NULL, updated_at TEXT NOT NULL
);
CREATE TABLE quota_simulation (
 account_id TEXT NOT NULL REFERENCES account(id), scope TEXT NOT NULL, source_instance_id TEXT NOT NULL,
 revision INTEGER NOT NULL, usage_bytes INTEGER NOT NULL CHECK(usage_bytes>=0), expires_at TEXT NOT NULL,
 PRIMARY KEY(account_id,scope,source_instance_id)
);
CREATE TABLE quota_rule_state (
 rule_id TEXT PRIMARY KEY REFERENCES quota_rule(id), evaluation_key TEXT NOT NULL, active INTEGER NOT NULL,
 generation INTEGER NOT NULL, signature TEXT NOT NULL, last_result_json TEXT NOT NULL
);
CREATE TABLE quota_decision (
 id INTEGER PRIMARY KEY AUTOINCREMENT, rule_id TEXT NOT NULL REFERENCES quota_rule(id),
 evaluated_at TEXT NOT NULL, payload_json TEXT NOT NULL
);
CREATE INDEX quota_decision_rule ON quota_decision(rule_id,id DESC);
CREATE TABLE quota_power_job (
 job_id TEXT PRIMARY KEY REFERENCES job(id), rule_id TEXT NOT NULL REFERENCES quota_rule(id),
 rule_revision INTEGER NOT NULL, evaluation_key TEXT NOT NULL, generation INTEGER NOT NULL,
 final_notified INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE automatic_power_action (
 job_id TEXT PRIMARY KEY REFERENCES job(id), instance_id TEXT NOT NULL REFERENCES instance(id), dispatch_started_at TEXT NOT NULL
);
CREATE INDEX automatic_power_window ON automatic_power_action(instance_id,dispatch_started_at);
CREATE TABLE quota_exemption (
 instance_id TEXT PRIMARY KEY REFERENCES instance(id), expires_at TEXT NOT NULL, source TEXT NOT NULL
);
