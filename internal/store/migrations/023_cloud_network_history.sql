CREATE TABLE cloud_network_sample (
 sequence INTEGER PRIMARY KEY AUTOINCREMENT,
 instance_id TEXT NOT NULL REFERENCES instance(id),
 source TEXT NOT NULL CHECK(source IN ('cms','swas_describe_monitor_data')),
 network_type TEXT NOT NULL CHECK(network_type IN ('classic','vpc','swas')),
 ip TEXT NOT NULL,
 direction TEXT NOT NULL CHECK(direction IN ('ingress','egress')),
 metric_name TEXT NOT NULL,
 period_seconds INTEGER NOT NULL CHECK(period_seconds=60),
 sampled_at TEXT NOT NULL,
 value REAL NOT NULL CHECK(value>=0),
 checked_at TEXT NOT NULL,
 UNIQUE(instance_id,source,network_type,ip,direction,sampled_at)
);
CREATE INDEX cloud_network_sample_range ON cloud_network_sample(instance_id,sampled_at DESC,sequence DESC);
