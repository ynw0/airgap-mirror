package sqlite

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"
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
	return initSchema(ctx, db, maintenanceSchema)
}

func InitClient(ctx context.Context, db *sql.DB) error { return initSchema(ctx, db, clientSchema) }

func initSchema(ctx context.Context, db *sql.DB, schema string) error {
	if _, err := db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("initialize sqlite schema: %w", err)
	}
	return nil
}
