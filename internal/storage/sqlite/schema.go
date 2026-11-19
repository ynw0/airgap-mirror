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
	return initSchema(ctx, db, maintenanceSchema)
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
	if strings.Contains(ddl, "'PAUSED'") && strings.Contains(ddl, "completed_projects") {
		return nil
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, stmt := range []string{
		"DROP TRIGGER IF EXISTS trg_epoch_reject_active_maintenance",
		"DROP TRIGGER IF EXISTS trg_maintenance_reject_active_epoch",
		`CREATE TABLE maintenance_jobs_next(
			id TEXT PRIMARY KEY,
			source_id TEXT NOT NULL REFERENCES sources(id) ON DELETE CASCADE,
			kind TEXT NOT NULL CHECK(kind IN('INVENTORY','GC')),
			status TEXT NOT NULL CHECK(status IN('QUEUED','RUNNING','PAUSED','COMPLETED','FAILED','CANCELLED')),
			execute INTEGER NOT NULL CHECK(execute IN(0,1)),
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
		`INSERT INTO maintenance_jobs_next(id,source_id,kind,status,execute,scanned_objects,scanned_bytes,candidate_objects,candidate_bytes,affected_objects,affected_bytes,temp_catalog_path,created_at,started_at,completed_at,error_text)
		SELECT id,source_id,kind,status,execute,scanned_objects,scanned_bytes,candidate_objects,candidate_bytes,affected_objects,affected_bytes,temp_catalog_path,created_at,started_at,completed_at,error_text FROM maintenance_jobs`,
		"DROP TABLE maintenance_jobs",
		"ALTER TABLE maintenance_jobs_next RENAME TO maintenance_jobs",
	} {
		if _, err = tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("migrate maintenance jobs: %w", err)
		}
	}
	return tx.Commit()
}
