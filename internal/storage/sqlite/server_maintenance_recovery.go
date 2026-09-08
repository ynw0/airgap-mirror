package sqlite

import (
	"context"
	"time"

	"github.com/ynw0/airgap-mirror/internal/domain"
)

func (s *ServerStore) RecoverInterruptedMaintenanceJobs(ctx context.Context, at time.Time, message string) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE maintenance_jobs SET status=?,error_text=?,completed_at=? WHERE status IN(?,?)`, domain.MaintenanceFailed, message, timeString(at), domain.MaintenanceQueued, domain.MaintenanceRunning)
	return err
}
