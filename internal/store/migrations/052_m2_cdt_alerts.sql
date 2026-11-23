INSERT OR IGNORE INTO alert_rule(key, enabled, params_json, urgent) VALUES
	('cdt_usage', 1, '{"thresholds_percent":[80,90,95]}', 1),
	('cdt_forecast_exceed', 1, '{}', 0);
