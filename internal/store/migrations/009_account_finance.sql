CREATE TABLE account_finance (
 account_id TEXT NOT NULL REFERENCES account(id),
 credential_version INTEGER NOT NULL,
 source TEXT NOT NULL CHECK(source IN ('balance','bill')),
 billing_cycle TEXT NOT NULL,
 fetched_at TEXT,
 last_attempt_at TEXT NOT NULL,
 error_code TEXT,
 payload_json TEXT,
 PRIMARY KEY(account_id,credential_version,source,billing_cycle),
 CHECK((source='balance' AND billing_cycle='') OR (source='bill' AND length(billing_cycle)=7))
);
