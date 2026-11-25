package sqlite

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"
	"strings"
)

//go:embed server_schema.sql
var serverSchema string

//go:embed maintenance_schema.sql
var maintenanceSchema string

//go:embed client_schema.sql
var clientSchema string

func InitServer(ctx context.Context, db *sql.DB) error {
	if err := initSchema(ctx, db, serverSchema); err != nil {
		return err
	}
	if err := migrateMaintenanceSchema(ctx, db); err != nil {
		return err
	}
	if err := initSchema(ctx, db, maintenanceSchema); err != nil {
		return err
	}
	if err := migrateIntegritySchema(ctx, db); err != nil {
		return err
	}
	return migrateIncrementalInventory(ctx, db)
}

func InitClient(ctx context.Context, db *sql.DB) error { return initSchema(ctx, db, clientSchema) }

func initSchema(ctx context.Context, db *sql.DB, schema string) error {
	if _, err := db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("initialize sqlite schema: %w", err)
	}
	return nil
}

// migrateMaintenanceSchema upgrades a previously installed Agent in place without
// touching sources, live Catalog, epochs, or repository trees.
func migrateMaintenanceSchema(ctx context.Context, db *sql.DB) error {
	var ddl string
	err := db.QueryRowContext(ctx, "SELECT sql FROM sqlite_master WHERE type='table' AND name='maintenance_jobs'").Scan(&ddl)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	if strings.Contains(ddl, "'PAUSED'") && strings.Contains(ddl, "completed_projects") && strings.Contains(ddl, "'REPAIR'") {
		return nil
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	projectColumns := ""
	if strings.Contains(ddl, "completed_projects") {
		projectColumns = ",completed_projects,total_projects,last_project"
	}
	if strings.Contains(ddl, "hash_mib_per_second") {
		projectColumns += ",hash_mib_per_second"
	}
	for _, stmt := range []string{
		"DROP TRIGGER IF EXISTS trg_epoch_reject_active_maintenance",
		"DROP TRIGGER IF EXISTS trg_maintenance_reject_active_epoch",
		`CREATE TABLE maintenance_jobs_next(
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
		)`,
		`INSERT INTO maintenance_jobs_next(id,source_id,kind,status,execute,scanned_objects,scanned_bytes,candidate_objects,candidate_bytes,affected_objects,affected_bytes,temp_catalog_path,created_at,started_at,completed_at,error_text` + projectColumns + `)
		SELECT id,source_id,kind,status,execute,scanned_objects,scanned_bytes,candidate_objects,candidate_bytes,affected_objects,affected_bytes,temp_catalog_path,created_at,started_at,completed_at,error_text` + projectColumns + ` FROM maintenance_jobs`,
		"DROP TABLE maintenance_jobs",
		"ALTER TABLE maintenance_jobs_next RENAME TO maintenance_jobs",
	} {
		if _, err = tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("migrate maintenance jobs: %w", err)
		}
	}
	return tx.Commit()
}

// Migrate only the repair report schema, not the large live catalog. The
// existing maintenance table gains an additive mode field with default 0.
func migrateIntegritySchema(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var ddl string
	if err = tx.QueryRowContext(ctx, "SELECT sql FROM sqlite_master WHERE type='table' AND name='maintenance_jobs'").Scan(&ddl); err != nil {
		return err
	}
	if !strings.Contains(ddl, "hash_mib_per_second") {
		if _, err = tx.ExecContext(ctx, "ALTER TABLE maintenance_jobs ADD COLUMN hash_mib_per_second INTEGER NOT NULL DEFAULT 0 CHECK(hash_mib_per_second BETWEEN 0 AND 1024)"); err != nil {
			return err
		}
	}
	if err = tx.QueryRowContext(ctx, "SELECT sql FROM sqlite_master WHERE type='table' AND name='repair_issues'").Scan(&ddl); err != nil {
		return err
	}
	if !strings.Contains(ddl, "'HASH_MISMATCH'") {
		for _, stmt := range []string{
			`CREATE TABLE repair_issues_next(
			 job_id TEXT NOT NULL REFERENCES repair_scans(job_id) ON DELETE CASCADE,
			 logical_path TEXT NOT NULL,package_key TEXT NOT NULL,size INTEGER NOT NULL,sha256 TEXT NOT NULL,
			 kind TEXT NOT NULL CHECK(kind IN('MISSING','SIZE_MISMATCH','HASH_MISMATCH')),
			 actual_size INTEGER NOT NULL,actual_sha256 TEXT NOT NULL DEFAULT '',
			 PRIMARY KEY(job_id,logical_path)) WITHOUT ROWID`,
			`INSERT INTO repair_issues_next(job_id,logical_path,package_key,size,sha256,kind,actual_size)
			 SELECT job_id,logical_path,package_key,size,sha256,kind,actual_size FROM repair_issues`,
			"DROP TABLE repair_issues",
			"ALTER TABLE repair_issues_next RENAME TO repair_issues",
			"CREATE INDEX ix_repair_issue_kind ON repair_issues(job_id,kind,package_key)",
		} {
			if _, err = tx.ExecContext(ctx, stmt); err != nil {
				return fmt.Errorf("migrate repair reports: %w", err)
			}
		}
	}
	return tx.Commit()
}

func migrateIncrementalInventory(ctx context.Context, db *sql.DB) error {
	var ddl string
	if err := db.QueryRowContext(ctx, "SELECT sql FROM sqlite_master WHERE type='table' AND name='maintenance_jobs'").Scan(&ddl); err != nil {
		return err
	}
	if strings.Contains(ddl, "incremental_inventory") {
		return nil
	}
	_, err := db.ExecContext(ctx, "ALTER TABLE maintenance_jobs ADD COLUMN incremental_inventory INTEGER NOT NULL DEFAULT 0 CHECK(incremental_inventory IN(0,1))")
	return err
}
