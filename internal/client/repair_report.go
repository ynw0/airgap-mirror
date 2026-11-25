package client

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/ynw0/airgap-mirror/internal/domain"
)

// WriteRepairReport exports an existing completed report in bounded pages.
// The caller owns the output and must discard it if this function returns an error.
func (c *AgentClient) WriteRepairReport(ctx context.Context, jobID string, dst io.Writer, progress func(int64, int64)) (int64, error) {
	job, err := c.Maintenance(ctx, jobID)
	if err != nil {
		return 0, err
	}
	if job.ID != jobID || job.Kind != domain.MaintenanceRepair || job.Status != domain.MaintenanceCompleted || job.CandidateObjects < 0 {
		return 0, fmt.Errorf("repair report must be completed: %w", domain.ErrConflict)
	}
	header, err := json.Marshal(struct {
		Kind       string    `json:"kind"`
		Scope      string    `json:"scope"`
		ExportedAt time.Time `json:"exportedAt"`
		SourceID   string    `json:"sourceId"`
		JobID      string    `json:"jobId"`
		Total      int64     `json:"total"`
	}{"repair", "complete", time.Now().UTC(), job.SourceID, job.ID, job.CandidateObjects})
	if err != nil {
		return 0, err
	}
	if _, err = dst.Write(append(header[:len(header)-1], []byte(",\"items\":[\n")...)); err != nil {
		return 0, err
	}
	var count int64
	after := ""
	if progress != nil {
		progress(0, job.CandidateObjects)
	}
	for {
		if err = ctx.Err(); err != nil {
			return count, err
		}
		pageCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
		page, pageErr := c.RepairIssues(pageCtx, jobID, after, 500)
		cancel()
		if pageErr != nil {
			return count, pageErr
		}
		last := after
		for _, item := range page.Items {
			if item.LogicalPath <= last || count >= job.CandidateObjects {
				return count, fmt.Errorf("repair report entries changed or are not ordered: %w", domain.ErrConflict)
			}
			if count > 0 {
				if _, err = io.WriteString(dst, ",\n"); err != nil {
					return count, err
				}
			}
			data, marshalErr := json.Marshal(item)
			if marshalErr != nil {
				return count, marshalErr
			}
			if _, err = dst.Write(data); err != nil {
				return count, err
			}
			last = item.LogicalPath
			count++
		}
		if progress != nil {
			progress(count, job.CandidateObjects)
		}
		if page.Next == "" {
			break
		}
		if page.Next != last || last <= after {
			return count, fmt.Errorf("repair report cursor did not advance: %w", domain.ErrConflict)
		}
		after = last
	}
	if count != job.CandidateObjects {
		return count, fmt.Errorf("repair report count %d differs from expected %d: %w", count, job.CandidateObjects, domain.ErrConflict)
	}
	if err = ctx.Err(); err != nil {
		return count, err
	}
	_, err = io.WriteString(dst, "\n]}\n")
	return count, err
}
