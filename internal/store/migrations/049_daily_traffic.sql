-- Keep observed billing graphs: discovery times are not provider attachment times.
CREATE TABLE daily_topology (
 id INTEGER PRIMARY KEY, account_id TEXT NOT NULL REFERENCES account(id), region TEXT NOT NULL,
 credential_version INTEGER NOT NULL, fingerprint TEXT NOT NULL,
 observed_from TEXT NOT NULL, last_seen TEXT NOT NULL, closed_at TEXT,
 subjects_json TEXT NOT NULL
);
CREATE INDEX daily_topology_window ON daily_topology(account_id,credential_version,region,observed_from);
CREATE UNIQUE INDEX daily_topology_open ON daily_topology(account_id,region) WHERE closed_at IS NULL;
CREATE TABLE daily_work (
 account_id TEXT NOT NULL REFERENCES account(id), timezone TEXT NOT NULL, day TEXT NOT NULL,
 generation INTEGER NOT NULL DEFAULT 1, pending INTEGER NOT NULL DEFAULT 1,
 PRIMARY KEY(account_id,timezone,day)
);
CREATE TABLE daily_traffic (
 account_id TEXT NOT NULL REFERENCES account(id), timezone TEXT NOT NULL, day TEXT NOT NULL,
 credential_version INTEGER NOT NULL, account_revision INTEGER NOT NULL,
 generation INTEGER NOT NULL, calculated_at TEXT NOT NULL, payload_json TEXT NOT NULL,
 PRIMARY KEY(account_id,timezone,day)
);
-- A receipt and its invalidation are atomic; cumulative recovery can repair old days.
CREATE TABLE daily_event (
 id INTEGER PRIMARY KEY AUTOINCREMENT, account_id TEXT NOT NULL,
 agent_id TEXT, counter_epoch TEXT, sequence INTEGER, cutoff TEXT
);
CREATE TRIGGER daily_sample_received AFTER INSERT ON traffic_sample_detail BEGIN
 INSERT INTO daily_event(account_id,agent_id,counter_epoch,sequence)
 SELECT i.account_id,NEW.agent_id,NEW.counter_epoch,NEW.sequence
 FROM agent a JOIN instance i ON i.id=a.instance_id WHERE a.id=NEW.agent_id;
END;
CREATE TRIGGER daily_official_received AFTER INSERT ON cdt_observation
 WHEN NEW.error_code IS NULL AND NEW.source_quality='verified' AND NEW.covered_until IS NOT NULL BEGIN
 INSERT INTO daily_event(account_id,cutoff) VALUES(NEW.account_id,NEW.covered_until);
END;
