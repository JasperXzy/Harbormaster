CREATE TABLE instance_event_state (
 account_id TEXT NOT NULL REFERENCES account(id),
 instance_id TEXT NOT NULL,
 kind TEXT NOT NULL CHECK(kind IN ('ecs','swas')),
 region TEXT NOT NULL, cloud_id TEXT NOT NULL, name TEXT NOT NULL,
 present INTEGER NOT NULL CHECK(present IN (0,1)),
 value_json TEXT NOT NULL,
 PRIMARY KEY(account_id,instance_id)
);
CREATE TABLE instance_event (
 sequence INTEGER PRIMARY KEY AUTOINCREMENT,
 id TEXT NOT NULL UNIQUE,
 account_id TEXT NOT NULL REFERENCES account(id),
 instance_id TEXT NOT NULL,
 payload_json TEXT NOT NULL
);
CREATE INDEX instance_event_account ON instance_event(account_id,sequence);
CREATE INDEX instance_event_instance ON instance_event(instance_id,sequence);
