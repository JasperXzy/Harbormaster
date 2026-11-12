CREATE TABLE instance_intent (
 instance_id TEXT PRIMARY KEY REFERENCES instance(id),
 job_id TEXT NOT NULL REFERENCES job(id),
 updated_at TEXT NOT NULL
);
-- Earlier versions only wrote real intent when enqueueing power jobs. Do not
-- expose the original 'stopped' column default as a user decision.
INSERT INTO instance_intent(instance_id,job_id,updated_at)
 SELECT i.id,j.id,j.created_at FROM instance i JOIN job j ON j.id=(
 SELECT p.id FROM job p JOIN job_order o ON o.job_id=p.id
 WHERE p.type='instance_power' AND p.instance_id=i.id ORDER BY o.seq DESC LIMIT 1
 );
