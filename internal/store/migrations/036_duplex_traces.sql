CREATE TABLE duplex_trace (
 id TEXT PRIMARY KEY,
 request_key TEXT NOT NULL UNIQUE,
 input_json TEXT NOT NULL,
 from_instance_id TEXT NOT NULL REFERENCES instance(id),
 to_instance_id TEXT NOT NULL REFERENCES instance(id),
 from_json TEXT NOT NULL,
 to_json TEXT NOT NULL,
 created_at TEXT NOT NULL,
 CHECK(from_instance_id<>to_instance_id)
);
CREATE TABLE duplex_trace_leg (
 duplex_id TEXT NOT NULL REFERENCES duplex_trace(id) ON DELETE CASCADE,
 direction TEXT NOT NULL CHECK(direction IN ('forward','reverse')),
 trace_seq INTEGER NOT NULL UNIQUE REFERENCES network_trace_run(seq),
 PRIMARY KEY(duplex_id,direction)
);
