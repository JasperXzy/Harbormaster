CREATE TABLE account_finance_next (
 account_id TEXT NOT NULL REFERENCES account(id),
 credential_version INTEGER NOT NULL,
 source TEXT NOT NULL CHECK(source IN ('balance','bill','instance_bill')),
 billing_cycle TEXT NOT NULL,
 fetched_at TEXT,
 last_attempt_at TEXT NOT NULL,
 error_code TEXT,
 payload_json TEXT,
 PRIMARY KEY(account_id,credential_version,source,billing_cycle),
 CHECK((source='balance' AND billing_cycle='') OR (source IN ('bill','instance_bill') AND length(billing_cycle)=7))
);
INSERT INTO account_finance_next SELECT * FROM account_finance;
DROP TABLE account_finance;
ALTER TABLE account_finance_next RENAME TO account_finance;
