ALTER TABLE agent_counter_state ADD COLUMN report_interval_seconds INTEGER NOT NULL DEFAULT 10 CHECK(report_interval_seconds BETWEEN 1 AND 60);
ALTER TABLE traffic_sample_detail ADD COLUMN report_interval_seconds INTEGER NOT NULL DEFAULT 10 CHECK(report_interval_seconds BETWEEN 1 AND 60);
