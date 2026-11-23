ALTER TABLE agent ADD COLUMN registered_at TEXT;
ALTER TABLE agent ADD COLUMN metadata_instance_id TEXT;
ALTER TABLE agent ADD COLUMN metadata_region_id TEXT;
CREATE TABLE agent_registration (
 instance_id TEXT PRIMARY KEY REFERENCES instance(id),
 code_hash BLOB NOT NULL UNIQUE CHECK(length(code_hash)=32),
 expires_at TEXT NOT NULL,
 consumed_at TEXT,
 nonce_hash BLOB,
 agent_id TEXT REFERENCES agent(id)
);
CREATE TABLE agent_counter_state (
 agent_id TEXT PRIMARY KEY REFERENCES agent(id),
 counter_epoch TEXT NOT NULL, sequence INTEGER NOT NULL,
 uptime_ms INTEGER NOT NULL, rx_bytes INTEGER NOT NULL, tx_bytes INTEGER NOT NULL,
 sampled_at TEXT NOT NULL, received_at TEXT NOT NULL,
 rules_version TEXT NOT NULL DEFAULT '',
 quality TEXT NOT NULL DEFAULT 'baseline'
);
INSERT INTO agent_counter_state(agent_id,counter_epoch,sequence,uptime_ms,rx_bytes,tx_bytes,sampled_at,received_at)
 SELECT agent_id,counter_epoch,sequence,uptime_ms,rx_bytes,tx_bytes,sampled_at,received_at
 FROM (SELECT *,row_number() OVER(PARTITION BY agent_id ORDER BY received_at DESC,sequence DESC) AS rownum FROM traffic_sample) WHERE rownum=1;
CREATE TABLE agent_retired_epoch (
 agent_id TEXT NOT NULL REFERENCES agent(id), counter_epoch TEXT NOT NULL,
 retired_at TEXT NOT NULL, PRIMARY KEY(agent_id,counter_epoch)
);
INSERT INTO agent_retired_epoch(agent_id,counter_epoch,retired_at)
 SELECT t.agent_id,t.counter_epoch,max(t.received_at) FROM traffic_sample t
 JOIN agent_counter_state s ON s.agent_id=t.agent_id WHERE t.counter_epoch<>s.counter_epoch GROUP BY t.agent_id,t.counter_epoch;
CREATE TABLE traffic_sample_detail (
 agent_id TEXT NOT NULL,counter_epoch TEXT NOT NULL,sequence INTEGER NOT NULL,
 rules_version TEXT NOT NULL,dropped_samples INTEGER NOT NULL,family_json TEXT,agent_version TEXT NOT NULL,quality TEXT NOT NULL,
 PRIMARY KEY(agent_id,counter_epoch,sequence),
 FOREIGN KEY(agent_id,counter_epoch,sequence) REFERENCES traffic_sample(agent_id,counter_epoch,sequence) ON DELETE CASCADE
);
