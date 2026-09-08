package domain

import (
	"fmt"
	"time"
)

type MaintenanceKind string

const (
	MaintenanceInventory MaintenanceKind = "INVENTORY"
	MaintenanceGC        MaintenanceKind = "GC"
)

type MaintenanceStatus string

const (
	MaintenanceQueued    MaintenanceStatus = "QUEUED"
	MaintenanceRunning   MaintenanceStatus = "RUNNING"
	MaintenanceCompleted MaintenanceStatus = "COMPLETED"
	MaintenanceFailed    MaintenanceStatus = "FAILED"
	MaintenanceCancelled MaintenanceStatus = "CANCELLED"
)

type MaintenanceJob struct {
	ID               string            `json:"id"`
	SourceID         string            `json:"sourceId"`
	Kind             MaintenanceKind   `json:"kind"`
	Status           MaintenanceStatus `json:"status"`
	Execute          bool              `json:"execute"`
	ScannedObjects   int64             `json:"scannedObjects"`
	ScannedBytes     int64             `json:"scannedBytes"`
	CandidateObjects int64             `json:"candidateObjects"`
	CandidateBytes   int64             `json:"candidateBytes"`
	AffectedObjects  int64             `json:"affectedObjects"`
	AffectedBytes    int64             `json:"affectedBytes"`
	TempCatalogPath  string            `json:"tempCatalogPath,omitempty"`
	CreatedAt        time.Time         `json:"createdAt"`
	StartedAt        *time.Time        `json:"startedAt,omitempty"`
	CompletedAt      *time.Time        `json:"completedAt,omitempty"`
	ErrorText        string            `json:"errorText,omitempty"`
}

func ValidateMaintenanceTransition(from, to MaintenanceStatus) error {
	allowed := false
	switch from {
	case MaintenanceQueued:
		allowed = to == MaintenanceRunning || to == MaintenanceCancelled || to == MaintenanceFailed
	case MaintenanceRunning:
		allowed = to == MaintenanceCompleted || to == MaintenanceCancelled || to == MaintenanceFailed
	case MaintenanceCompleted, MaintenanceFailed, MaintenanceCancelled:
		allowed = false
	default:
		return fmt.Errorf("unknown maintenance status %q: %w", from, ErrInvalid)
	}
	if !allowed {
		return fmt.Errorf("maintenance transition %s -> %s is not allowed: %w", from, to, ErrConflict)
	}
	return nil
}
