CREATE TABLE power_schedule (
	id TEXT PRIMARY KEY,
	revision INTEGER NOT NULL,
	enabled INTEGER NOT NULL CHECK(enabled IN (0,1)),
	account_id TEXT NOT NULL REFERENCES account(id),
	config_json TEXT NOT NULL,
	activated_at TEXT NOT NULL,
	last_triggered_at TEXT,
	deleted INTEGER NOT NULL DEFAULT 0 CHECK(deleted IN (0,1)),
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
);

CREATE TABLE power_schedule_run (
	schedule_id TEXT NOT NULL REFERENCES power_schedule(id),
	window_key TEXT NOT NULL,
	triggered_at TEXT NOT NULL,
	PRIMARY KEY(schedule_id, window_key)
);

CREATE TABLE keepalive_settings (
	id INTEGER PRIMARY KEY CHECK(id = 1),
	revision INTEGER NOT NULL,
	config_json TEXT NOT NULL,
	updated_at TEXT
);

INSERT INTO keepalive_settings(id, revision, config_json, updated_at)
VALUES (1, 0, '{"enabled":false,"dry_run":false,"spot_auto_recover":true,"target_instance_ids":[]}', NULL);

CREATE TABLE monthly_reset_settings (
	id INTEGER PRIMARY KEY CHECK(id = 1),
	revision INTEGER NOT NULL,
	config_json TEXT NOT NULL,
	last_cycle_start TEXT,
	updated_at TEXT
);

INSERT INTO monthly_reset_settings(id, revision, config_json, last_cycle_start, updated_at)
VALUES (1, 0, '{"enabled":false,"dry_run":false,"target_instance_ids":[]}', NULL, NULL);

CREATE TABLE instance_intent_confirmation (
	instance_id TEXT PRIMARY KEY REFERENCES instance(id) ON DELETE CASCADE,
	account_id TEXT NOT NULL,
	detected_at TEXT NOT NULL,
	status TEXT NOT NULL CHECK(status IN ('pending','keep_stopped','restore_running')),
	resolved_at TEXT,
	resolved_by TEXT
);

CREATE TABLE automation_power_job (
	job_id TEXT PRIMARY KEY REFERENCES job(id),
	kind TEXT NOT NULL CHECK(kind IN ('schedule','keepalive','monthly_reset')),
	rule_id TEXT NOT NULL,
	rule_revision INTEGER NOT NULL,
	window_key TEXT NOT NULL,
	final_notified INTEGER NOT NULL DEFAULT 0 CHECK(final_notified IN (0,1))
);
