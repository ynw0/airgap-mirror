package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/ynw0/airgap-mirror/internal/domain"
	"github.com/ynw0/airgap-mirror/internal/ports"
)

type controlTestJobs struct {
	ports.MaintenanceStore
	mu      sync.Mutex
	job     domain.MaintenanceJob
	entered chan struct{}
	release chan struct{}
}

func (s *controlTestJobs) GetMaintenanceJob(_ context.Context, _ string) (domain.MaintenanceJob, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.job, nil
}

func (s *controlTestJobs) TransitionMaintenanceJob(ctx context.Context, _ string, to domain.MaintenanceStatus, message string, _ time.Time) error {
	if s.entered != nil {
		close(s.entered)
		select {
		case <-s.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := domain.ValidateMaintenanceTransition(s.job.Status, to); err != nil {
		return err
	}
	s.job.Status = to
	s.job.ErrorText = message
	return nil
}

type controlTestSources struct {
	ports.ServerStore
}

func (controlTestSources) GetSource(_ context.Context, id string) (domain.Source, error) {
	return domain.Source{ID: id, Type: domain.SourcePyPI, Provider: "pypi"}, nil
}

func (controlTestSources) GetState(_ context.Context, _ string) (domain.SourceState, error) {
	// A resume based on stale PAUSED state must not panic in the test double.
	// If it gets here during cancellation, return a distinct error.
	return domain.SourceState{}, errors.New("unexpected stale resume state query")
}

func TestCancelIntentCannotBeDowngradedToPause(t *testing.T) {
	jobs := &controlTestJobs{job: domain.MaintenanceJob{ID: "job", SourceID: "source", Kind: domain.MaintenanceInventory, Status: domain.MaintenanceRunning}}
	r := NewMaintenanceRunner(context.Background(), controlTestSources{}, jobs, nil, nil, nil)
	workerCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.cancels["job"] = cancel

	if _, err := r.Cancel(context.Background(), "job"); err != nil {
		t.Fatal(err)
	}
	if workerCtx.Err() != context.Canceled {
		t.Fatal("worker cancellation was not signalled")
	}
	if _, err := r.PauseInventory(context.Background(), "job"); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("pause after cancel must conflict, got %v", err)
	}
	r.finishInventoryError("job", true, context.Canceled)
	job, err := jobs.GetMaintenanceJob(context.Background(), "job")
	if err != nil || job.Status != domain.MaintenanceCancelled {
		t.Fatalf("cancel was not preserved: %+v, %v", job, err)
	}
}

func TestPausedCancelExcludesConcurrentResume(t *testing.T) {
	jobs := &controlTestJobs{
		job:     domain.MaintenanceJob{ID: "job", SourceID: "source", Kind: domain.MaintenanceInventory, Status: domain.MaintenancePaused},
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	r := NewMaintenanceRunner(context.Background(), controlTestSources{}, jobs, nil, nil, nil)
	cancelResult := make(chan error, 1)
	go func() {
		_, err := r.Cancel(context.Background(), "job")
		cancelResult <- err
	}()
	select {
	case <-jobs.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("cancel did not reach the state transition")
	}

	resumeResult := make(chan error, 1)
	go func() {
		_, err := r.ResumeInventory(context.Background(), "job")
		resumeResult <- err
	}()
	// Cancellation is inside the durable state transition. Resume must wait
	// rather than read PAUSED and start a worker on a checkpoint being discarded.
	select {
	case err := <-resumeResult:
		close(jobs.release)
		t.Fatalf("resume returned before cancel finished: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(jobs.release)
	select {
	case err := <-cancelResult:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancel did not finish")
	}
	select {
	case err := <-resumeResult:
		if !errors.Is(err, domain.ErrConflict) {
			t.Fatalf("resume must reject cancelled job, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("resume did not finish")
	}
}
