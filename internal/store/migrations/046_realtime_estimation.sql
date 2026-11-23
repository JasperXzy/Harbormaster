ALTER TABLE cdt_observation ADD COLUMN covered_until TEXT;
ALTER TABLE cdt_observation ADD COLUMN period_start TEXT;
ALTER TABLE cdt_observation ADD COLUMN period_end TEXT;
ALTER TABLE cdt_observation ADD COLUMN source_quality TEXT NOT NULL DEFAULT 'inferred' CHECK(source_quality IN ('verified','inferred','unknown'));
ALTER TABLE cdt_observation ADD COLUMN source TEXT NOT NULL DEFAULT 'ListCdtInternetTraffic';
CREATE TABLE metering_settings (
 id INTEGER PRIMARY KEY CHECK(id=1), revision INTEGER NOT NULL DEFAULT 0,
 lag_seconds INTEGER NOT NULL DEFAULT 3600 CHECK(lag_seconds BETWEEN 60 AND 604800),
 bandwidth_ids_json TEXT NOT NULL DEFAULT '[]'
);
INSERT INTO metering_settings(id) VALUES(1);
-- Observations are not authoritative attachment timestamps. Verified membership
-- is reserved for a future source that supplies such evidence, never a UI flag.
CREATE TABLE metering_topology (
 account_id TEXT NOT NULL REFERENCES account(id), region TEXT NOT NULL,
 credential_version INTEGER NOT NULL, fingerprint TEXT NOT NULL,
 observed_since TEXT NOT NULL, last_seen TEXT NOT NULL, complete INTEGER NOT NULL,
 quality TEXT NOT NULL DEFAULT 'observed' CHECK(quality IN ('observed','verified')),
 PRIMARY KEY(account_id,region)
);
CREATE TABLE traffic_estimate (
 account_id TEXT NOT NULL REFERENCES account(id), scope TEXT NOT NULL CHECK(scope IN ('mainland','overseas')),
 credential_version INTEGER NOT NULL, account_revision INTEGER NOT NULL,
 settings_revision INTEGER NOT NULL, basis_id TEXT NOT NULL, calculated_at TEXT NOT NULL,
 payload_json TEXT NOT NULL,
 PRIMARY KEY(account_id,scope)
);
