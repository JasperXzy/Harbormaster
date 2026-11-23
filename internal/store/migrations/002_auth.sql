CREATE TABLE setup_credential (
 id INTEGER PRIMARY KEY CHECK(id=1),
 token_hash BLOB NOT NULL CHECK(length(token_hash)=32),
 expires_at TEXT NOT NULL
);
CREATE TABLE auth_attempt (
 bucket TEXT PRIMARY KEY,
 attempts INTEGER NOT NULL CHECK(attempts>0),
 expires_at INTEGER NOT NULL
);
CREATE INDEX auth_attempt_expiry ON auth_attempt(expires_at);
