CREATE TABLE instance_metadata (
	instance_id TEXT PRIMARY KEY REFERENCES instance(id) ON DELETE CASCADE,
	note TEXT NOT NULL DEFAULT '',
	group_name TEXT,
	tags_json TEXT NOT NULL DEFAULT '[]',
	updated_at TEXT NOT NULL
);

CREATE INDEX instance_metadata_group ON instance_metadata(group_name);
