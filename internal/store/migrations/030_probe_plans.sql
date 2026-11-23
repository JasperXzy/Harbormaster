CREATE TABLE network_target (
 id TEXT PRIMARY KEY,
 revision INTEGER NOT NULL,
 config_json TEXT NOT NULL,
 enabled INTEGER NOT NULL CHECK(enabled IN (0,1)),
 deleted INTEGER NOT NULL DEFAULT 0 CHECK(deleted IN (0,1))
);
CREATE TABLE probe_plan (
 id TEXT PRIMARY KEY,
 revision INTEGER NOT NULL,
 target_id TEXT NOT NULL REFERENCES network_target(id),
 config_json TEXT NOT NULL,
 enabled INTEGER NOT NULL CHECK(enabled IN (0,1)),
 deleted INTEGER NOT NULL DEFAULT 0 CHECK(deleted IN (0,1)),
 next_at INTEGER NOT NULL
);
CREATE INDEX probe_plan_due ON probe_plan(enabled,deleted,next_at);
CREATE TABLE probe_plan_run (
 seq INTEGER PRIMARY KEY AUTOINCREMENT,
 plan_id TEXT NOT NULL REFERENCES probe_plan(id),
 plan_revision INTEGER NOT NULL,
 target_id TEXT NOT NULL REFERENCES network_target(id),
 target_revision INTEGER NOT NULL,
 point_kind TEXT NOT NULL CHECK(point_kind IN ('server','agent')),
 instance_id TEXT REFERENCES instance(id),
 created_at TEXT NOT NULL,
 job_id TEXT REFERENCES job(id),
 skip_reason TEXT,
 CHECK((job_id IS NULL AND skip_reason IS NOT NULL) OR (job_id IS NOT NULL AND skip_reason IS NULL))
);
CREATE INDEX probe_plan_run_history ON probe_plan_run(plan_id,seq DESC);
CREATE TABLE instance_probe_address (
 instance_id TEXT PRIMARY KEY REFERENCES instance(id),
 credential_version INTEGER NOT NULL,
 observed_at TEXT NOT NULL,
 ipv4_json TEXT,
 ipv6_json TEXT
);
-- These are editable initial targets, never enabled measurement schedules.
INSERT INTO network_target(id,revision,enabled,config_json)
 SELECT 'target_'||key,1,1,json_object('id','target_'||key,'revision',1,'name',host,'kind','custom','host',host,'instance_id',NULL,'tcp_port',443,'enabled',json('true'),'created_at',strftime('%Y-%m-%dT%H:%M:%fZ','now'),'updated_at',strftime('%Y-%m-%dT%H:%M:%fZ','now'))
 FROM (SELECT 'cloudflare_dns' AS key,'1.1.1.1' AS host UNION ALL SELECT 'google_dns','8.8.8.8' UNION ALL SELECT 'cloudflare','cloudflare.com' UNION ALL SELECT 'google','google.com');
CREATE UNIQUE INDEX probe_plan_run_job ON probe_plan_run(job_id) WHERE job_id IS NOT NULL;
CREATE INDEX probe_plan_run_time ON probe_plan_run(plan_id,created_at);
CREATE TABLE agent_probe_contact (
 agent_id TEXT PRIMARY KEY REFERENCES agent(id),
 token_hash BLOB NOT NULL CHECK(length(token_hash)=32),
 last_poll_at TEXT NOT NULL
);
