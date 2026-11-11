CREATE TABLE telegram_channel (
 id INTEGER PRIMARY KEY CHECK(id=1),
 ciphertext BLOB NOT NULL CHECK(length(ciphertext)>=29),
 revision INTEGER NOT NULL CHECK(revision>0),
 updated_at TEXT NOT NULL,
 last_success_at TEXT,
 last_error_code TEXT
);
CREATE TABLE notification (
 seq INTEGER PRIMARY KEY AUTOINCREMENT,
 id TEXT NOT NULL UNIQUE,
 event_key TEXT NOT NULL UNIQUE,
 job_id TEXT REFERENCES job(id),
 created_at TEXT NOT NULL,
 kind TEXT NOT NULL CHECK(kind IN ('alert','recovery','test')),
 severity TEXT NOT NULL CHECK(severity IN ('info','warning','serious','critical')),
 rule_key TEXT,
 title TEXT NOT NULL, body TEXT NOT NULL,
 account_id TEXT REFERENCES account(id), instance_id TEXT REFERENCES instance(id),
 delivery_status TEXT NOT NULL CHECK(delivery_status IN ('pending','sent','failed','not_sent')),
 channel_revision INTEGER NOT NULL DEFAULT 0,
 available_at INTEGER NOT NULL DEFAULT 0,
 attempts INTEGER NOT NULL DEFAULT 0,
 lease_owner TEXT, lease_until INTEGER NOT NULL DEFAULT 0,
 last_error_code TEXT, uncertain INTEGER NOT NULL DEFAULT 0 CHECK(uncertain IN (0,1)),
 delivered_at TEXT, message_id INTEGER
);
CREATE INDEX notification_dispatch ON notification(delivery_status,available_at,lease_until);
CREATE TABLE notification_change (seq INTEGER PRIMARY KEY AUTOINCREMENT,notification_id TEXT NOT NULL REFERENCES notification(id));
CREATE TRIGGER notification_created AFTER INSERT ON notification
 BEGIN INSERT INTO notification_change(notification_id) VALUES(new.id); END;
CREATE TRIGGER notification_updated AFTER UPDATE OF delivery_status,last_error_code,uncertain ON notification
 WHEN old.delivery_status<>new.delivery_status OR old.last_error_code IS NOT new.last_error_code OR old.uncertain<>new.uncertain
 BEGIN INSERT INTO notification_change(notification_id) VALUES(new.id); END;
