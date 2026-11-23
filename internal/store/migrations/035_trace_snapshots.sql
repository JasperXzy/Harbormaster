CREATE TABLE trace_schedule (
 plan_id TEXT PRIMARY KEY REFERENCES probe_plan(id),
 revision INTEGER NOT NULL,
 config_json TEXT NOT NULL,
 enabled INTEGER NOT NULL CHECK(enabled IN (0,1)),
 timezone TEXT NOT NULL,
 next_at INTEGER NOT NULL
);
CREATE INDEX trace_schedule_due ON trace_schedule(enabled,next_at);
CREATE TABLE trace_snapshot_run (
 seq INTEGER PRIMARY KEY AUTOINCREMENT,
 plan_id TEXT NOT NULL REFERENCES probe_plan(id),
 plan_name TEXT NOT NULL,
 plan_revision INTEGER NOT NULL,
 schedule_revision INTEGER NOT NULL,
 target_id TEXT NOT NULL REFERENCES network_target(id),
 target_revision INTEGER NOT NULL,
 due_at TEXT NOT NULL,
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL,
 trace_seq INTEGER UNIQUE REFERENCES network_trace_run(seq),
 skip_reason TEXT,
 notify_changes INTEGER NOT NULL CHECK(notify_changes IN (0,1)),
 comparison_json TEXT,
 CHECK((trace_seq IS NULL AND skip_reason IS NOT NULL) OR (trace_seq IS NOT NULL AND skip_reason IS NULL)),
 UNIQUE(plan_id,schedule_revision,due_at)
);
CREATE INDEX trace_snapshot_history ON trace_snapshot_run(plan_id,seq DESC);
CREATE INDEX trace_snapshot_time ON trace_snapshot_run(plan_id,created_at);
CREATE INDEX trace_snapshot_pending ON trace_snapshot_run(seq) WHERE comparison_json IS NULL;
INSERT INTO alert_rule VALUES('network_route_change',1,'{}',0);
