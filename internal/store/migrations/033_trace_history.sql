CREATE TABLE agent_trace_contact (
 agent_id TEXT PRIMARY KEY REFERENCES agent(id),
 token_hash BLOB NOT NULL CHECK(length(token_hash)=32),
 last_poll_at TEXT NOT NULL
);
CREATE TABLE network_trace_run (
 seq INTEGER PRIMARY KEY AUTOINCREMENT,
 job_id TEXT NOT NULL UNIQUE REFERENCES job(id),
 input_json TEXT NOT NULL,
 point_id TEXT NOT NULL,
 point_name TEXT NOT NULL,
 target_id TEXT REFERENCES network_target(id),
 target_revision INTEGER,
 target_json TEXT,
 created_at TEXT NOT NULL,
 outcome_json TEXT,
 CHECK((target_id IS NULL AND target_revision IS NULL AND target_json IS NULL)
 OR (target_id IS NOT NULL AND target_revision IS NOT NULL AND target_revision>0 AND target_json IS NOT NULL))
);
CREATE INDEX network_trace_time ON network_trace_run(created_at,seq);
CREATE INDEX network_trace_point ON network_trace_run(point_id,seq DESC);
CREATE INDEX network_trace_target ON network_trace_run(target_id,seq DESC);
