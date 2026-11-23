INSERT OR IGNORE INTO alert_rule(key,enabled,params_json,urgent) VALUES
  ('swas_traffic_usage',1,'{"thresholds_percent":[80,90,95]}',1),
  ('cpu_credit_low',1,'{"threshold_percent":20}',0),
  ('system_event',1,'{}',0);

CREATE TABLE IF NOT EXISTS version_check_cache (
  id INTEGER PRIMARY KEY CHECK(id=1),
  checked_at TEXT NOT NULL,
  latest_version TEXT,
  release_url TEXT,
  release_notes TEXT,
  error_code TEXT
);
