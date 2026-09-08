CREATE TABLE IF NOT EXISTS maintenance_jobs(
  id TEXT PRIMARY KEY,
  source_id TEXT NOT NULL REFERENCES sources(id) ON DELETE CASCADE,
  kind TEXT NOT NULL CHECK(kind IN('INVENTORY','GC')),
  status TEXT NOT NULL CHECK(status IN('QUEUED','RUNNING','COMPLETED','FAILED','CANCELLED')),
  execute INTEGER NOT NULL CHECK(execute IN(0,1)),
  scanned_objects INTEGER NOT NULL DEFAULT 0,
  scanned_bytes INTEGER NOT NULL DEFAULT 0,
  candidate_objects INTEGER NOT NULL DEFAULT 0,
  candidate_bytes INTEGER NOT NULL DEFAULT 0,
  affected_objects INTEGER NOT NULL DEFAULT 0,
  affected_bytes INTEGER NOT NULL DEFAULT 0,
  temp_catalog_path TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  started_at TEXT,
  completed_at TEXT,
  error_text TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS ix_maintenance_source_created
  ON maintenance_jobs(source_id,created_at DESC);
CREATE UNIQUE INDEX IF NOT EXISTS ux_maintenance_active_source
  ON maintenance_jobs(source_id)
  WHERE status IN('QUEUED','RUNNING');
