CREATE TABLE IF NOT EXISTS maintenance_jobs(
  id TEXT PRIMARY KEY,
  source_id TEXT NOT NULL REFERENCES sources(id) ON DELETE CASCADE,
  kind TEXT NOT NULL CHECK(kind IN('INVENTORY','GC','REPAIR')),
  status TEXT NOT NULL CHECK(status IN('QUEUED','RUNNING','PAUSED','COMPLETED','FAILED','CANCELLED')),
  execute INTEGER NOT NULL CHECK(execute IN(0,1)),
  hash_mib_per_second INTEGER NOT NULL DEFAULT 0 CHECK(hash_mib_per_second BETWEEN 0 AND 1024),
  scanned_objects INTEGER NOT NULL DEFAULT 0,
  scanned_bytes INTEGER NOT NULL DEFAULT 0,
  completed_projects INTEGER NOT NULL DEFAULT 0,
  total_projects INTEGER NOT NULL DEFAULT 0,
  last_project TEXT NOT NULL DEFAULT '',
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
  WHERE status IN('QUEUED','RUNNING','PAUSED');

-- Maintenance and Epoch mutation are mutually exclusive at the database boundary.
-- The triggers close the race between an application-level preflight and the final write.
CREATE TRIGGER IF NOT EXISTS trg_maintenance_reject_active_epoch
BEFORE INSERT ON maintenance_jobs
WHEN NEW.status IN('QUEUED','RUNNING','PAUSED')
 AND COALESCE((SELECT active_epoch_id FROM source_states WHERE source_id=NEW.source_id),'') <> ''
BEGIN
  SELECT RAISE(ABORT, 'source has active epoch; maintenance is blocked');
END;

CREATE TRIGGER IF NOT EXISTS trg_epoch_reject_active_maintenance
BEFORE UPDATE OF active_epoch_id ON source_states
WHEN COALESCE(NEW.active_epoch_id,'') <> ''
 AND EXISTS(
   SELECT 1 FROM maintenance_jobs
   WHERE source_id=NEW.source_id AND status IN('QUEUED','RUNNING','PAUSED')
 )
BEGIN
  SELECT RAISE(ABORT, 'source has active maintenance job; epoch is blocked');
END;

CREATE TABLE IF NOT EXISTS repair_scans(
 job_id TEXT PRIMARY KEY REFERENCES maintenance_jobs(id) ON DELETE CASCADE,
 catalog_version INTEGER NOT NULL,
 source_updated_at TEXT NOT NULL,
 last_path TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS repair_issues(
 job_id TEXT NOT NULL REFERENCES repair_scans(job_id) ON DELETE CASCADE,
 logical_path TEXT NOT NULL,
 package_key TEXT NOT NULL,
 size INTEGER NOT NULL,
 sha256 TEXT NOT NULL,
 kind TEXT NOT NULL CHECK(kind IN('MISSING','SIZE_MISMATCH','HASH_MISMATCH')),
 actual_size INTEGER NOT NULL,
 actual_sha256 TEXT NOT NULL DEFAULT '',
 PRIMARY KEY(job_id,logical_path)
) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS ix_repair_issue_kind ON repair_issues(job_id,kind,package_key);

-- Explicit administrator approval is scoped to a completed integrity report.
-- The report's source revision and Catalog version are checked again at import.
CREATE TABLE IF NOT EXISTS repair_replacement_approvals(
 job_id TEXT PRIMARY KEY REFERENCES repair_scans(job_id) ON DELETE CASCADE,
 approved_at TEXT NOT NULL
);
