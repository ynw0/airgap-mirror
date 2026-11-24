PRAGMA foreign_keys=ON;
PRAGMA journal_mode=WAL;
PRAGMA synchronous=NORMAL;
PRAGMA busy_timeout=10000;

CREATE TABLE IF NOT EXISTS schema_migrations(
  version INTEGER PRIMARY KEY,
  applied_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS sources(
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  type TEXT NOT NULL CHECK(type IN('apt','pypi','npm','maven')),
  provider TEXT NOT NULL,
  upstream_url TEXT NOT NULL,
  root_path TEXT NOT NULL,
  public_url TEXT NOT NULL,
  enabled INTEGER NOT NULL CHECK(enabled IN(0,1)),
  config_json BLOB NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS source_states(
  source_id TEXT PRIMARY KEY REFERENCES sources(id) ON DELETE CASCADE,
  cursor_kind TEXT NOT NULL,
  cursor_value TEXT NOT NULL,
  catalog_version INTEGER NOT NULL DEFAULT 0,
  live_bytes INTEGER NOT NULL DEFAULT 0,
  live_objects INTEGER NOT NULL DEFAULT 0,
  active_epoch_id TEXT,
  updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS epochs(
  id TEXT PRIMARY KEY,
  source_id TEXT NOT NULL REFERENCES sources(id),
  base_cursor_kind TEXT NOT NULL,
  base_cursor_value TEXT NOT NULL,
  target_cursor_kind TEXT NOT NULL,
  target_cursor_value TEXT NOT NULL,
  status TEXT NOT NULL,
  total_bytes INTEGER NOT NULL,
  total_objects INTEGER NOT NULL,
  total_batches INTEGER NOT NULL,
  publish_unit_count INTEGER NOT NULL,
  created_at TEXT NOT NULL,
  completed_at TEXT,
  error_text TEXT NOT NULL DEFAULT ''
);
CREATE UNIQUE INDEX IF NOT EXISTS ux_epochs_active_source
  ON epochs(source_id) WHERE status NOT IN('COMPLETE','FAILED','CANCELLED');

CREATE TABLE IF NOT EXISTS batches(
  id TEXT PRIMARY KEY,
  epoch_id TEXT NOT NULL REFERENCES epochs(id),
  source_id TEXT NOT NULL REFERENCES sources(id),
  sequence INTEGER NOT NULL,
  status TEXT NOT NULL,
  planned_bytes INTEGER NOT NULL,
  object_count INTEGER NOT NULL,
  pack_count INTEGER NOT NULL,
  created_at TEXT NOT NULL,
  imported_at TEXT,
  error_text TEXT NOT NULL DEFAULT '',
  UNIQUE(epoch_id,sequence)
);

CREATE TABLE IF NOT EXISTS packs(
  id TEXT PRIMARY KEY,
  epoch_id TEXT NOT NULL REFERENCES epochs(id),
  batch_id TEXT NOT NULL REFERENCES batches(id),
  sequence INTEGER NOT NULL,
  size INTEGER NOT NULL,
  sha256 TEXT NOT NULL,
  entry_count INTEGER NOT NULL,
  uploaded_size INTEGER NOT NULL DEFAULT 0,
  file_path TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL,
  UNIQUE(batch_id,sequence)
);

CREATE TABLE IF NOT EXISTS publish_units(
  id TEXT PRIMARY KEY,
  epoch_id TEXT NOT NULL REFERENCES epochs(id),
  source_id TEXT NOT NULL REFERENCES sources(id),
  unit_key TEXT NOT NULL,
  status TEXT NOT NULL,
  required_count INTEGER NOT NULL,
  imported_count INTEGER NOT NULL DEFAULT 0,
  metadata_path TEXT NOT NULL DEFAULT '',
  published_at TEXT,
  UNIQUE(epoch_id,unit_key)
);
CREATE INDEX IF NOT EXISTS ix_publish_units_ready
  ON publish_units(epoch_id,status,imported_count,required_count);

CREATE TABLE IF NOT EXISTS publish_metadata_entries(
  entry_id TEXT PRIMARY KEY,
  publish_unit_id TEXT NOT NULL REFERENCES publish_units(id),
  source_id TEXT NOT NULL,
  logical_path TEXT NOT NULL,
  size INTEGER NOT NULL,
  sha256 TEXT NOT NULL,
  operation TEXT NOT NULL,
  staged_path TEXT NOT NULL,
  package_key TEXT NOT NULL DEFAULT '',
  version TEXT NOT NULL DEFAULT '',
  attributes BLOB,
  UNIQUE(publish_unit_id,logical_path)
);

CREATE TABLE IF NOT EXISTS catalog_entries(
  source_id TEXT NOT NULL REFERENCES sources(id) ON DELETE CASCADE,
  logical_path TEXT NOT NULL,
  size INTEGER NOT NULL,
  sha256 TEXT NOT NULL,
  package_key TEXT NOT NULL DEFAULT '',
  version TEXT NOT NULL DEFAULT '',
  attributes BLOB,
  updated_at TEXT NOT NULL,
  PRIMARY KEY(source_id,logical_path)
) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS ix_catalog_package ON catalog_entries(source_id,package_key);

CREATE TABLE IF NOT EXISTS import_sessions(
  id TEXT PRIMARY KEY,
  request_id TEXT NOT NULL UNIQUE,
  source_id TEXT NOT NULL REFERENCES sources(id),
  epoch_id TEXT NOT NULL REFERENCES epochs(id),
  batch_id TEXT NOT NULL REFERENCES batches(id),
  manifest_expected_size INTEGER NOT NULL,
  manifest_expected_sha256 TEXT NOT NULL,
  manifest_uploaded_size INTEGER NOT NULL DEFAULT 0,
  manifest_path TEXT NOT NULL,
  staging_path TEXT NOT NULL,
  status TEXT NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  error_text TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS import_packs(
  session_id TEXT NOT NULL REFERENCES import_sessions(id) ON DELETE CASCADE,
  pack_id TEXT NOT NULL REFERENCES packs(id),
  expected_size INTEGER NOT NULL,
  expected_sha256 TEXT NOT NULL,
  uploaded_size INTEGER NOT NULL DEFAULT 0,
  staging_path TEXT NOT NULL,
  status TEXT NOT NULL,
  PRIMARY KEY(session_id,pack_id)
);

CREATE TABLE IF NOT EXISTS imported_entries(
  epoch_id TEXT NOT NULL,
  entry_id TEXT NOT NULL,
  source_id TEXT NOT NULL,
  logical_path TEXT NOT NULL,
  sha256 TEXT NOT NULL,
  publish_unit_id TEXT NOT NULL,
  metadata INTEGER NOT NULL CHECK(metadata IN(0,1)),
  imported_at TEXT NOT NULL,
  PRIMARY KEY(epoch_id,entry_id)
) WITHOUT ROWID;

CREATE TABLE IF NOT EXISTS audit_logs(
  id TEXT PRIMARY KEY,
  kind TEXT NOT NULL,
  source_id TEXT NOT NULL DEFAULT '',
  epoch_id TEXT NOT NULL DEFAULT '',
  batch_id TEXT NOT NULL DEFAULT '',
  message TEXT NOT NULL,
  detail BLOB,
  created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_audit_created ON audit_logs(created_at);

-- Catalog generations are additive: upgraded databases keep their existing
-- catalog_entries as the initial live generation without a full migration.
CREATE TABLE IF NOT EXISTS catalog_generations(
  source_id TEXT NOT NULL REFERENCES sources(id) ON DELETE CASCADE,
  generation TEXT NOT NULL,
  expected_objects INTEGER NOT NULL,
  expected_bytes INTEGER NOT NULL,
  copied_objects INTEGER NOT NULL DEFAULT 0,
  copied_bytes INTEGER NOT NULL DEFAULT 0,
  last_path TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL CHECK(status IN('BUILDING','LIVE','RETIRED')),
  PRIMARY KEY(source_id,generation)
) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS catalog_generation_entries(
  source_id TEXT NOT NULL,
  generation TEXT NOT NULL,
  logical_path TEXT NOT NULL,
  size INTEGER NOT NULL,
  sha256 TEXT NOT NULL,
  package_key TEXT NOT NULL DEFAULT '',
  version TEXT NOT NULL DEFAULT '',
  attributes BLOB,
  updated_at TEXT NOT NULL,
  PRIMARY KEY(source_id,generation,logical_path),
  FOREIGN KEY(source_id,generation) REFERENCES catalog_generations(source_id,generation) ON DELETE CASCADE
) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS ix_generation_package ON catalog_generation_entries(source_id,generation,package_key);
CREATE TABLE IF NOT EXISTS catalog_active_generation(
  source_id TEXT PRIMARY KEY REFERENCES sources(id) ON DELETE CASCADE,
  generation TEXT NOT NULL,
  FOREIGN KEY(source_id,generation) REFERENCES catalog_generations(source_id,generation)
);
CREATE VIEW IF NOT EXISTS live_catalog_entries AS
 SELECT c.source_id,c.logical_path,c.size,c.sha256,c.package_key,c.version,c.attributes,c.updated_at
 FROM catalog_entries c WHERE NOT EXISTS(SELECT 1 FROM catalog_active_generation a WHERE a.source_id=c.source_id)
 UNION ALL
 SELECT c.source_id,c.logical_path,c.size,c.sha256,c.package_key,c.version,c.attributes,c.updated_at
 FROM catalog_generation_entries c JOIN catalog_active_generation a
 ON a.source_id=c.source_id AND a.generation=c.generation;

CREATE TABLE IF NOT EXISTS state_export_jobs(
 id TEXT PRIMARY KEY,
 source_ids BLOB NOT NULL,
 repair_job_id TEXT NOT NULL DEFAULT '',
 status TEXT NOT NULL CHECK(status IN('QUEUED','RUNNING','COMPLETED','FAILED')),
 stage TEXT NOT NULL DEFAULT '',
 completed_sources INTEGER NOT NULL DEFAULT 0,
 total_sources INTEGER NOT NULL,
 result_json BLOB,
 error_text TEXT NOT NULL DEFAULT '',
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_state_export_status ON state_export_jobs(status,created_at);
