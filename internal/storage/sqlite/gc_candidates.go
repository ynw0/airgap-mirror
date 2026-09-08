package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"

	"github.com/ynw0/airgap-mirror/internal/domain"
	"github.com/ynw0/airgap-mirror/internal/ports"
)

type GCCandidateFactory struct{ Root string }

func NewGCCandidateFactory(root string) *GCCandidateFactory { return &GCCandidateFactory{Root: root} }

type gcCandidateWriter struct {
	path   string
	db     *sql.DB
	tx     *sql.Tx
	stmt   *sql.Stmt
	closed bool
}

type gcCandidateReader struct {
	path string
	db   *sql.DB
}

func (f *GCCandidateFactory) root() (string, error) {
	if strings.TrimSpace(f.Root) == "" {
		return "", fmt.Errorf("GC candidate root is required: %w", domain.ErrInvalid)
	}
	root, err := filepath.Abs(f.Root)
	if err != nil {
		return "", err
	}
	if err = os.MkdirAll(root, 0750); err != nil {
		return "", err
	}
	return root, nil
}

func (f *GCCandidateFactory) Create(ctx context.Context, jobID string) (ports.GCCandidateSink, error) {
	root, err := f.root()
	if err != nil {
		return nil, err
	}
	if jobID == "" || strings.ContainsAny(jobID, `/\\`) {
		return nil, fmt.Errorf("unsafe GC job id %q: %w", jobID, domain.ErrInvalid)
	}
	path := filepath.Join(root, "gc-"+jobID+".sqlite")
	if _, err = os.Stat(path); err == nil {
		return nil, fmt.Errorf("GC candidate database already exists: %w", domain.ErrConflict)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	cleanup := func() {
		_ = db.Close()
		_ = os.Remove(path)
	}
	if _, err = db.ExecContext(ctx, `PRAGMA journal_mode=OFF; PRAGMA synchronous=OFF; CREATE TABLE candidates(logical_path TEXT PRIMARY KEY,size INTEGER NOT NULL,mtime_ns INTEGER NOT NULL) WITHOUT ROWID;`); err != nil {
		cleanup()
		return nil, fmt.Errorf("initialize GC candidate database: %w", err)
	}
	if err = os.Chmod(path, 0640); err != nil {
		cleanup()
		return nil, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		cleanup()
		return nil, err
	}
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO candidates(logical_path,size,mtime_ns) VALUES(?,?,?)`)
	if err != nil {
		_ = tx.Rollback()
		cleanup()
		return nil, err
	}
	return &gcCandidateWriter{path: path, db: db, tx: tx, stmt: stmt}, nil
}

func (f *GCCandidateFactory) Open(ctx context.Context, candidatePath string) (ports.GCCandidateReader, error) {
	root, err := f.root()
	if err != nil {
		return nil, err
	}
	abs, err := filepath.Abs(candidatePath)
	if err != nil {
		return nil, err
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.Dir(rel) != "." || !strings.HasPrefix(filepath.Base(rel), "gc-") || !strings.HasSuffix(rel, ".sqlite") {
		return nil, fmt.Errorf("GC candidate path is outside maintenance root: %w", domain.ErrInvalid)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("GC candidate database is not a regular file: %w", domain.ErrInvalid)
	}
	u := &url.URL{Scheme: "file", Path: filepath.ToSlash(abs)}
	db, err := sql.Open("sqlite", u.String()+"?mode=ro&immutable=1")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	var table string
	if err = db.QueryRowContext(ctx, `SELECT name FROM sqlite_master WHERE type='table' AND name='candidates'`).Scan(&table); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("validate GC candidate database: %w", err)
	}
	return &gcCandidateReader{path: abs, db: db}, nil
}

func (w *gcCandidateWriter) Put(ctx context.Context, candidate domain.GCCandidate) error {
	if w.closed || candidate.LogicalPath == "" || candidate.Size < 0 || candidate.ModTimeUnixNano < 0 {
		return fmt.Errorf("invalid GC candidate %q: %w", candidate.LogicalPath, domain.ErrInvalid)
	}
	_, err := w.stmt.ExecContext(ctx, candidate.LogicalPath, candidate.Size, candidate.ModTimeUnixNano)
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "unique") {
		return fmt.Errorf("duplicate GC candidate %s: %w", candidate.LogicalPath, domain.ErrConflict)
	}
	return err
}

func (w *gcCandidateWriter) Stats(ctx context.Context) (domain.GCStats, error) {
	var out domain.GCStats
	if w.closed {
		return out, fmt.Errorf("GC candidate writer is closed: %w", domain.ErrConflict)
	}
	err := w.tx.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(size),0) FROM candidates`).Scan(&out.Objects, &out.Bytes)
	return out, err
}

func (w *gcCandidateWriter) Path() string { return w.path }

func (w *gcCandidateWriter) Close() error {
	if w.closed {
		return nil
	}
	w.closed = true
	_ = w.stmt.Close()
	if err := w.tx.Commit(); err != nil {
		_ = w.db.Close()
		return err
	}
	return w.db.Close()
}

func (w *gcCandidateWriter) Abort() error {
	if !w.closed {
		w.closed = true
		if w.stmt != nil {
			_ = w.stmt.Close()
		}
		if w.tx != nil {
			_ = w.tx.Rollback()
		}
		if w.db != nil {
			_ = w.db.Close()
		}
	}
	err := os.Remove(w.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func gcStats(ctx context.Context, db *sql.DB) (domain.GCStats, error) {
	var out domain.GCStats
	err := db.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(size),0) FROM candidates`).Scan(&out.Objects, &out.Bytes)
	return out, err
}

func (r *gcCandidateReader) Stats(ctx context.Context) (domain.GCStats, error) {
	return gcStats(ctx, r.db)
}

func (r *gcCandidateReader) List(ctx context.Context, after string, limit int) ([]domain.GCCandidate, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	rows, err := r.db.QueryContext(ctx, `SELECT logical_path,size,mtime_ns FROM candidates WHERE logical_path>? ORDER BY logical_path LIMIT ?`, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]domain.GCCandidate, 0)
	for rows.Next() {
		var v domain.GCCandidate
		if err = rows.Scan(&v.LogicalPath, &v.Size, &v.ModTimeUnixNano); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (r *gcCandidateReader) Walk(ctx context.Context, fn func(domain.GCCandidate) error) error {
	rows, err := r.db.QueryContext(ctx, `SELECT logical_path,size,mtime_ns FROM candidates ORDER BY logical_path`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err = ctx.Err(); err != nil {
			return err
		}
		var v domain.GCCandidate
		if err = rows.Scan(&v.LogicalPath, &v.Size, &v.ModTimeUnixNano); err != nil {
			return err
		}
		if err = fn(v); err != nil {
			return err
		}
	}
	return rows.Err()
}

func (r *gcCandidateReader) Close() error { return r.db.Close() }

var _ ports.GCCandidateFactory = (*GCCandidateFactory)(nil)
var _ ports.GCCandidateSink = (*gcCandidateWriter)(nil)
var _ ports.GCCandidateReader = (*gcCandidateReader)(nil)
