-- Passkey private keys stay on authenticators. These records contain public
-- verification material only; no reversible authentication secret is stored.
CREATE TABLE passkey_state (
 id INTEGER PRIMARY KEY CHECK(id=1),
 user_handle BLOB NOT NULL CHECK(length(user_handle)=32),
 epoch INTEGER NOT NULL DEFAULT 1 CHECK(epoch>0)
);
INSERT INTO passkey_state(id,user_handle) VALUES(1,randomblob(32));
CREATE TABLE passkey (
 id TEXT PRIMARY KEY,
 credential_id BLOB NOT NULL UNIQUE,
 name TEXT NOT NULL,
 credential_json TEXT NOT NULL,
 created_at TEXT NOT NULL,
 last_used_at TEXT
);
CREATE TABLE passkey_ceremony (
 id TEXT PRIMARY KEY,
 kind TEXT NOT NULL CHECK(kind IN ('registration','login')),
 binding_hash BLOB NOT NULL CHECK(length(binding_hash)=32),
 epoch INTEGER NOT NULL,
 name TEXT NOT NULL,
 session_json TEXT NOT NULL,
 expires_at TEXT NOT NULL
);
CREATE INDEX passkey_ceremony_expiry ON passkey_ceremony(expires_at);
