CREATE TABLE panel_branding (
 id INTEGER PRIMARY KEY CHECK(id=1),
 name TEXT NOT NULL DEFAULT 'Harbormaster',
 revision INTEGER NOT NULL DEFAULT 0 CHECK(revision>=0),
 logo_png BLOB,
 logo_width INTEGER,
 logo_height INTEGER,
 updated_at TEXT,
 CHECK(
  (logo_png IS NULL AND logo_width IS NULL AND logo_height IS NULL) OR
  (logo_png IS NOT NULL AND length(logo_png) BETWEEN 1 AND 2097152
   AND logo_width IS NOT NULL AND logo_width BETWEEN 1 AND 512
   AND logo_height IS NOT NULL AND logo_height BETWEEN 1 AND 512)
 )
);
INSERT INTO panel_branding(id) VALUES(1);
