package domain

import (
	"fmt"
	"time"
)

type MaintenanceKind string

const (
	MaintenanceInventory MaintenanceKind = "INVENTORY"
	MaintenanceGC        MaintenanceKind = "GC"
	MaintenanceRepair    MaintenanceKind = "REPAIR"
)

type MaintenanceStatus string

const (
	MaintenanceQueued    MaintenanceStatus = "QUEUED"
	MaintenanceRunning   MaintenanceStatus = "RUNNING"
	MaintenancePaused    MaintenanceStatus = "PAUSED"
	MaintenanceCompleted MaintenanceStatus = "COMPLETED"
	MaintenanceFailed    MaintenanceStatus = "FAILED"
	MaintenanceCancelled MaintenanceStatus = "CANCELLED"
)

type MaintenanceJob struct {
	ID                string            `json:"id"`
	SourceID          string            `json:"sourceId"`
	Kind              MaintenanceKind   `json:"kind"`
	Status            MaintenanceStatus `json:"status"`
	Execute           bool              `json:"execute"`
	HashMiBPerSecond  int               `json:"hashMiBPerSecond,omitempty"`
	ScannedObjects    int64             `json:"scannedObjects"`
	ScannedBytes      int64             `json:"scannedBytes"`
	CompletedProjects int64             `json:"completedProjects"`
	TotalProjects     int64             `json:"totalProjects"`
	LastProject       string            `json:"lastProject,omitempty"`
	CandidateObjects  int64             `json:"candidateObjects"`
	CandidateBytes    int64             `json:"candidateBytes"`
	AffectedObjects   int64             `json:"affectedObjects"`
	AffectedBytes     int64             `json:"affectedBytes"`
	TempCatalogPath   string            `json:"tempCatalogPath,omitempty"`
	CreatedAt         time.Time         `json:"createdAt"`
	StartedAt         *time.Time        `json:"startedAt,omitempty"`
	CompletedAt       *time.Time        `json:"completedAt,omitempty"`
	ErrorText         string            `json:"errorText,omitempty"`
}

func ValidateMaintenanceTransition(from, to MaintenanceStatus) error {
	allowed := false
	switch from {
	case MaintenanceQueued:
		allowed = to == MaintenanceRunning || to == MaintenanceCancelled || to == MaintenanceFailed || to == MaintenancePaused
	case MaintenanceRunning:
		allowed = to == MaintenanceCompleted || to == MaintenanceCancelled || to == MaintenanceFailed || to == MaintenancePaused
	case MaintenancePaused:
		allowed = to == MaintenanceRunning || to == MaintenanceCancelled || to == MaintenanceFailed
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

type RepairIssue struct {
	LogicalPath  string `json:"logicalPath"`
	PackageKey   string `json:"packageKey"`
	Size         int64  `json:"size"`
	SHA256       string `json:"sha256"`
	Kind         string `json:"kind"`
	ActualSize   int64  `json:"actualSize"`
	ActualSHA256 string `json:"actualSha256,omitempty"`
}

type RepairCheckpoint struct {
	LastPath       string
	CatalogVersion int64
}

type CatalogReclaimResult struct {
	RemovedEntries     int64 `json:"removedEntries"`
	RemovedGenerations int64 `json:"removedGenerations"`
	More               bool  `json:"more"`
}

type RepairReplacementEvidence struct {
	JobID string
	LogicalPath string
	Size int64
	SHA256 string
	ActualSize int64
	ActualSHA256 string
}
