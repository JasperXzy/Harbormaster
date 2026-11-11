CREATE TABLE api_key (
 sequence INTEGER PRIMARY KEY,
 id TEXT NOT NULL UNIQUE,
 name TEXT NOT NULL,
 scopes_json TEXT NOT NULL,
 token_hash BLOB NOT NULL UNIQUE CHECK(length(token_hash)=32),
 created_at TEXT NOT NULL,
 last_used_at TEXT,
 revoked_at TEXT
);
