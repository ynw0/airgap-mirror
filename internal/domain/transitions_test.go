package domain

import "testing"

func TestEpochTransitionLinearPath(t *testing.T) {
	path := []EpochStatus{EpochPlanned, EpochDownloading, EpochTransferring, EpochPublishable, EpochPublished, EpochComplete}
	for i := 0; i < len(path)-1; i++ {
		if err := ValidateEpochTransition(path[i], path[i+1]); err != nil {
			t.Fatalf("%s -> %s: %v", path[i], path[i+1], err)
		}
	}
}

func TestEpochTransitionRejectsSkipAndTerminal(t *testing.T) {
	cases := []struct {
		from, to EpochStatus
		ok       bool
	}{
		{EpochPlanned, EpochPlanned, true},
		{EpochPlanned, EpochTransferring, false},
		{EpochDownloading, EpochFailed, true},
		{EpochDownloading, EpochCancelled, true},
		{EpochComplete, EpochFailed, false},
		{EpochComplete, EpochCancelled, false},
		{EpochCancelled, EpochFailed, false},
		{EpochFailed, EpochDownloading, false},
		{EpochPublished, EpochComplete, true},
	}
	for _, c := range cases {
		err := ValidateEpochTransition(c.from, c.to)
		if c.ok && err != nil {
			t.Fatalf("%s -> %s should be allowed: %v", c.from, c.to, err)
		}
		if !c.ok && err == nil {
			t.Fatalf("%s -> %s should be rejected", c.from, c.to)
		}
	}
}

func TestBatchAndPackTransitions(t *testing.T) {
	if err := ValidateBatchTransition(BatchPlanned, BatchDownloading); err != nil {
		t.Fatal(err)
	}
	if err := ValidateBatchTransition(BatchPlanned, BatchReady); err == nil {
		t.Fatal("batch skip should be rejected")
	}
	if err := ValidateBatchTransition(BatchImported, BatchFailed); err == nil {
		t.Fatal("imported batch cannot fail")
	}
	if err := ValidatePackTransition(PackPlanned, PackWriting); err != nil {
		t.Fatal(err)
	}
	if err := ValidatePackTransition(PackPlanned, PackReady); err == nil {
		t.Fatal("pack skip should be rejected")
	}
	if err := ValidatePackTransition(PackImported, PackFailed); err == nil {
		t.Fatal("imported pack cannot fail")
	}
}

func TestMaintenanceTransitions(t *testing.T) {
	if err := ValidateMaintenanceTransition(MaintenanceQueued, MaintenanceRunning); err != nil {
		t.Fatal(err)
	}
	if err := ValidateMaintenanceTransition(MaintenanceRunning, MaintenanceCompleted); err != nil {
		t.Fatal(err)
	}
	if err := ValidateMaintenanceTransition(MaintenanceCompleted, MaintenanceRunning); err == nil {
		t.Fatal("completed maintenance cannot restart")
	}
	if err := ValidateMaintenanceTransition(MaintenanceQueued, MaintenanceCompleted); err == nil {
		t.Fatal("queued cannot jump to completed")
	}
}
