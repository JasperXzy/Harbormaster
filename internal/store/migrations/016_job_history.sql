ALTER TABLE job ADD COLUMN trigger_source TEXT NOT NULL DEFAULT 'unknown'
 CHECK(trigger_source IN ('panel','cli','api','telegram','schedule','system','unknown'));
ALTER TABLE job ADD COLUMN instance_id TEXT REFERENCES instance(id);
CREATE TABLE job_order (
 seq INTEGER PRIMARY KEY AUTOINCREMENT,
 job_id TEXT NOT NULL UNIQUE REFERENCES job(id)
);
INSERT INTO job_order(job_id) SELECT id FROM job ORDER BY rowid;
CREATE TRIGGER job_order_created AFTER INSERT ON job
 BEGIN INSERT INTO job_order(job_id) VALUES(new.id); END;
