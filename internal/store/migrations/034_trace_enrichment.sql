CREATE TABLE trace_enrichment (
 trace_seq INTEGER PRIMARY KEY REFERENCES network_trace_run(seq) ON DELETE CASCADE,
 state TEXT NOT NULL CHECK(state IN ('queued','running','complete','partial','failed')),
 updated_at TEXT NOT NULL,
 lease_owner TEXT,
 lease_until INTEGER NOT NULL DEFAULT 0,
 result_json TEXT,
 error_code TEXT,
 CHECK((state IN ('complete','partial') AND result_json IS NOT NULL) OR (state NOT IN ('complete','partial') AND result_json IS NULL))
);
CREATE INDEX trace_enrichment_queue ON trace_enrichment(state,lease_until,trace_seq);
INSERT INTO trace_enrichment(trace_seq,state,updated_at)
 SELECT r.seq,'queued',coalesce(j.finished_at,r.created_at)
 FROM network_trace_run r JOIN job j ON j.id=r.job_id
 WHERE r.outcome_json IS NOT NULL;
