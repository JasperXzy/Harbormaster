CREATE TABLE hourly_work (
	agent_id TEXT NOT NULL,
	hour_start TEXT NOT NULL,
	pending INTEGER NOT NULL DEFAULT 1 CHECK(pending IN (0,1)),
	PRIMARY KEY(agent_id, hour_start)
);

CREATE TABLE agent_hourly_traffic (
	agent_id TEXT NOT NULL,
	instance_id TEXT NOT NULL,
	hour_start TEXT NOT NULL,
	rx_bytes INTEGER NOT NULL CHECK(rx_bytes >= 0),
	tx_bytes INTEGER NOT NULL CHECK(tx_bytes >= 0),
	covered_ms INTEGER NOT NULL CHECK(covered_ms >= 0),
	sample_count INTEGER NOT NULL CHECK(sample_count >= 0),
	prorated INTEGER NOT NULL CHECK(prorated IN (0,1)),
	updated_at TEXT NOT NULL,
	PRIMARY KEY(agent_id, hour_start)
);

CREATE INDEX agent_hourly_instance ON agent_hourly_traffic(instance_id, hour_start);

CREATE TABLE hourly_event (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	agent_id TEXT NOT NULL,
	counter_epoch TEXT NOT NULL,
	sequence INTEGER NOT NULL
);

CREATE TRIGGER hourly_sample_received AFTER INSERT ON traffic_sample_detail BEGIN
	INSERT INTO hourly_event(agent_id, counter_epoch, sequence)
	VALUES(NEW.agent_id, NEW.counter_epoch, NEW.sequence);
END;

INSERT OR IGNORE INTO hourly_work(agent_id, hour_start, pending)
SELECT DISTINCT agent_id, substr(sampled_at, 1, 13) || ':00:00Z', 1
FROM traffic_sample
WHERE length(sampled_at) >= 13;

CREATE TABLE traffic_pricing (
	id INTEGER PRIMARY KEY CHECK(id = 1),
	revision INTEGER NOT NULL,
	config_json TEXT NOT NULL,
	updated_at TEXT
);

INSERT INTO traffic_pricing(id, revision, config_json, updated_at) VALUES (
	1,
	0,
	'{"china":{"currency":"CNY","mainland_tiers":[{"up_to_gib":null,"unit_price":"0.80"}],"overseas_tiers":[{"up_to_gib":10240,"unit_price":"0.50"},{"up_to_gib":51200,"unit_price":"0.45"},{"up_to_gib":null,"unit_price":"0.40"}]},"international":{"currency":"USD","mainland_tiers":[{"up_to_gib":null,"unit_price":"0.125"}],"overseas_tiers":[{"up_to_gib":10240,"unit_price":"0.08"},{"up_to_gib":51200,"unit_price":"0.075"},{"up_to_gib":null,"unit_price":"0.07"}]}}',
	NULL
);
