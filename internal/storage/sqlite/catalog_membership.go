package sqlite

import (
	"context"
	"database/sql"
	"errors"

	"github.com/ynw0/airgap-mirror/internal/ports"
)

func (s *ServerStore) Contains(ctx context.Context, sourceID, logicalPath string) (bool, error) {
	var one int
	err := s.DB.QueryRowContext(ctx, `SELECT 1 FROM catalog_entries WHERE source_id=? AND logical_path=?`, sourceID, logicalPath).Scan(&one)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return false, err
}

var _ ports.CatalogMembership = (*ServerStore)(nil)
