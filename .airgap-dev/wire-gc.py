from pathlib import Path


def replace_once(path: str, old: str, new: str) -> None:
    p = Path(path)
    text = p.read_text(encoding="utf-8")
    if old not in text:
        raise SystemExit(f"patch marker not found in {path}: {old!r}")
    p.write_text(text.replace(old, new, 1), encoding="utf-8")


replace_once(
    "internal/server/maintenance.go",
    '\tmux.Handle("POST /api/v1/sources/{id}/inventory", secure(h.startInventory))\n\tmux.Handle("GET /api/v1/maintenance", secure(h.list))\n\tmux.Handle("GET /api/v1/maintenance/{id}", secure(h.get))\n',
    '\tmux.Handle("POST /api/v1/sources/{id}/inventory", secure(h.startInventory))\n\tmux.Handle("POST /api/v1/sources/{id}/gc", secure(h.startGC))\n\tmux.Handle("GET /api/v1/maintenance", secure(h.list))\n\tmux.Handle("GET /api/v1/maintenance/{id}", secure(h.get))\n\tmux.Handle("GET /api/v1/maintenance/{id}/candidates", secure(h.candidates))\n',
)

replace_once(
    "cmd/mirror-agent/main.go",
    '\tmaintenanceRunner := service.NewMaintenanceRunner(maintenanceCtx, store, store, store, registry, storesqlite.NewRebuildCatalogFactory(maintenance))\n',
    '\tmaintenanceRunner := service.NewMaintenanceRunner(maintenanceCtx, store, store, store, registry, storesqlite.NewRebuildCatalogFactory(maintenance))\n\tmaintenanceRunner.Candidates = storesqlite.NewGCCandidateFactory(maintenance)\n',
)

replace_once(
    "internal/storage/sqlite/server_maintenance.go",
    '''\t_, err := s.DB.ExecContext(ctx, `INSERT INTO maintenance_jobs(id,source_id,kind,status,execute,created_at) VALUES(?,?,?,?,?,?)`, job.ID, job.SourceID, job.Kind, job.Status, execute, timeString(job.CreatedAt))
\tif err != nil {
\t\tif strings.Contains(strings.ToLower(err.Error()), "unique constraint") {
\t\t\treturn fmt.Errorf("source already has an active maintenance job: %w", domain.ErrConflict)
\t\t}
\t\treturn err
\t}
''',
    '''\t_, err := s.DB.ExecContext(ctx, `INSERT INTO maintenance_jobs(id,source_id,kind,status,execute,created_at) VALUES(?,?,?,?,?,?)`, job.ID, job.SourceID, job.Kind, job.Status, execute, timeString(job.CreatedAt))
\tif err != nil {
\t\tlower := strings.ToLower(err.Error())
\t\tif strings.Contains(lower, "unique constraint") {
\t\t\treturn fmt.Errorf("source already has an active maintenance job: %w", domain.ErrConflict)
\t\t}
\t\tif strings.Contains(lower, "source has active epoch; maintenance is blocked") {
\t\t\treturn fmt.Errorf("source has an active epoch; maintenance is blocked: %w", domain.ErrConflict)
\t\t}
\t\treturn err
\t}
''',
)

replace_once(
    "internal/storage/sqlite/server_import_registration.go",
    '\t"fmt"\n',
    '\t"fmt"\n\t"strings"\n',
)
replace_once(
    "internal/storage/sqlite/server_import_registration.go",
    '''\t_, err = tx.ExecContext(ctx, `UPDATE source_states SET active_epoch_id=?,updated_at=? WHERE source_id=?`, d.EpochID, timeString(session.UpdatedAt), d.SourceID)
\tif err != nil {
\t\treturn domain.ImportSession{}, err
\t}
''',
    '''\t_, err = tx.ExecContext(ctx, `UPDATE source_states SET active_epoch_id=?,updated_at=? WHERE source_id=?`, d.EpochID, timeString(session.UpdatedAt), d.SourceID)
\tif err != nil {
\t\tif strings.Contains(strings.ToLower(err.Error()), "source has active maintenance job; epoch is blocked") {
\t\t\treturn domain.ImportSession{}, fmt.Errorf("source has an active maintenance job; import is blocked: %w", domain.ErrConflict)
\t\t}
\t\treturn domain.ImportSession{}, err
\t}
''',
)
