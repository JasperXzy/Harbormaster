CREATE TABLE cdt_reconciliation (
 id INTEGER PRIMARY KEY AUTOINCREMENT,account_id TEXT NOT NULL REFERENCES account(id),scope TEXT NOT NULL,
 credential_version INTEGER NOT NULL,account_revision INTEGER NOT NULL,cycle_start TEXT NOT NULL,observation_id INTEGER NOT NULL REFERENCES cdt_observation(id),
 payload_json TEXT NOT NULL,UNIQUE(account_id,scope,observation_id)
);
CREATE INDEX cdt_reconciliation_account ON cdt_reconciliation(account_id,scope,id DESC);
CREATE TABLE cdt_reconciliation_state (
 account_id TEXT NOT NULL REFERENCES account(id),scope TEXT NOT NULL,credential_version INTEGER NOT NULL,account_revision INTEGER NOT NULL,
 cycle_start TEXT NOT NULL,last_observation_id INTEGER NOT NULL,max_cutoff TEXT,
 bad_streak INTEGER NOT NULL,healthy_streak INTEGER NOT NULL,active INTEGER NOT NULL,episode INTEGER NOT NULL,
 PRIMARY KEY(account_id,scope)
);
INSERT INTO alert_rule VALUES('cdt_estimation_deviation',1,'{"consecutive_failures":3,"recovery_samples":3}',1);
