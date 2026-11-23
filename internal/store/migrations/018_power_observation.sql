ALTER TABLE instance_power_job ADD COLUMN baseline_json TEXT;
ALTER TABLE instance_power_job ADD COLUMN observation_json TEXT;
ALTER TABLE instance_power_job ADD COLUMN saw_transition INTEGER NOT NULL DEFAULT 0 CHECK(saw_transition IN (0,1));
ALTER TABLE instance_power_job ADD COLUMN confirmation_basis TEXT CHECK(confirmation_basis IN ('target_state','start_time_changed','state_transition'));
