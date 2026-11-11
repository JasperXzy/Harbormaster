CREATE TABLE alert_rule (
 key TEXT PRIMARY KEY,
 enabled INTEGER NOT NULL CHECK(enabled IN (0,1)),
 params_json TEXT NOT NULL,
 urgent INTEGER NOT NULL CHECK(urgent IN (0,1))
);
INSERT INTO alert_rule VALUES
 ('account_api_failure',1,'{"consecutive_failures":3}',0),
 ('cdt_official_unavailable',1,'{"consecutive_failures":3}',1),
 ('agent_offline',1,'{"offline_after_seconds":30}',1);
CREATE TABLE alert_state (
 rule_key TEXT NOT NULL REFERENCES alert_rule(key), target_id TEXT NOT NULL,
 account_id TEXT REFERENCES account(id),
 active INTEGER NOT NULL CHECK(active IN (0,1)), episode INTEGER NOT NULL,
 changed_at TEXT NOT NULL,
 PRIMARY KEY(rule_key,target_id)
);
CREATE INDEX cdt_observation_current ON cdt_observation(account_id,credential_version,id DESC);
CREATE INDEX notification_change_lookup ON notification_change(notification_id,seq);
