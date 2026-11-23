CREATE TABLE quiet_hours (
 id INTEGER PRIMARY KEY CHECK(id=1),
 revision INTEGER NOT NULL DEFAULT 0,
 enabled INTEGER NOT NULL DEFAULT 0 CHECK(enabled IN (0,1)),
 start_time TEXT NOT NULL DEFAULT '23:00',
 end_time TEXT NOT NULL DEFAULT '08:00',
 maintenance_start TEXT,
 maintenance_end TEXT,
 updated_at TEXT,
 CHECK((maintenance_start IS NULL)=(maintenance_end IS NULL))
);
INSERT INTO quiet_hours(id) VALUES(1);
ALTER TABLE notification ADD COLUMN urgent INTEGER NOT NULL DEFAULT 0 CHECK(urgent IN (0,1));
ALTER TABLE notification ADD COLUMN suppression_reason TEXT CHECK(suppression_reason IN ('daily','maintenance'));
ALTER TABLE notification ADD COLUMN suppressed_at TEXT;
ALTER TABLE notification ADD COLUMN suppression_revision INTEGER;
UPDATE notification SET urgent=1 WHERE kind='test' OR severity='critical'
 OR rule_key IN ('agent_offline','cdt_usage','cdt_forecast_exceed','cdt_official_unavailable')
 OR rule_key IN (SELECT key FROM alert_rule WHERE urgent=1);
