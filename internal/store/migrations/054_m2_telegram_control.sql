CREATE TABLE telegram_control (
	id INTEGER PRIMARY KEY CHECK(id = 1),
	revision INTEGER NOT NULL,
	enabled INTEGER NOT NULL CHECK(enabled IN (0,1)),
	allowed_user_ids_json TEXT NOT NULL,
	updated_at TEXT
);

INSERT INTO telegram_control(id, revision, enabled, allowed_user_ids_json, updated_at)
VALUES (1, 0, 0, '[]', NULL);

CREATE TABLE telegram_bot_offset (
	id INTEGER PRIMARY KEY CHECK(id = 1),
	channel_revision INTEGER NOT NULL,
	update_offset INTEGER NOT NULL
);

CREATE TABLE telegram_action_token (
	token TEXT PRIMARY KEY,
	user_id TEXT NOT NULL,
	chat_id TEXT NOT NULL,
	instance_id TEXT NOT NULL,
	action TEXT NOT NULL,
	stop_mode TEXT NOT NULL,
	expires_at TEXT NOT NULL
);
