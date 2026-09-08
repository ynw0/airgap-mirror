package sqlite

import (
	"database/sql"
	"errors"
	"time"

	"github.com/ynw0/airgap-mirror/internal/domain"
)

type ServerStore struct{ DB *sql.DB }

func NewServerStore(db *sql.DB) *ServerStore { return &ServerStore{DB: db} }

func timeString(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }
func parseTime(s string) time.Time  { t, _ := time.Parse(time.RFC3339Nano, s); return t }
func parseOptionalTime(s sql.NullString) *time.Time {
	if !s.Valid {
		return nil
	}
	t := parseTime(s.String)
	return &t
}
func mapNotFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ErrNotFound
	}
	return err
}
