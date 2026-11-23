ALTER TABLE alert_state ADD COLUMN basis TEXT NOT NULL DEFAULT '';
INSERT INTO alert_rule VALUES ('instance_expiring',1,'{"before_days":7}',0);
