package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/ynw0/airgap-mirror/internal/client"
	"github.com/ynw0/airgap-mirror/internal/domain"
	"github.com/ynw0/airgap-mirror/internal/service"
	storesqlite "github.com/ynw0/airgap-mirror/internal/storage/sqlite"
)

func TestExportJobsRecoverAfterRequestAndProcessInterruption(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "server.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err = storesqlite.InitServer(ctx, db); err != nil {
		t.Fatal(err)
	}
	store := storesqlite.NewServerStore(db)
	id, err := domain.NewID()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	src := domain.Source{ID: id, Name: "PyPI", Type: domain.SourcePyPI, Provider: "pypi", RootPath: t.TempDir(), UpstreamURL: "https://pypi.org/simple", PublicURL: "https://repo.example/pypi", Enabled: true, ConfigJSON: json.RawMessage("{}"), CreatedAt: now, UpdatedAt: now}
	if err = store.CreateSource(ctx, src); err != nil {
		t.Fatal(err)
	}
	if err = store.Upsert(ctx, domain.CatalogEntry{SourceID: id, LogicalPath: "packages/a.whl", Size: 1, SHA256: strings.Repeat("a", 64), UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	caps := service.CapsuleService{Store: store, Exporter: store, OutputDir: t.TempDir()}
	jobs := &service.ExportJobs{Store: store, Capsules: caps}
	api := (&API{Store: store, Catalog: store, Capsules: caps, ExportJobs: jobs, BearerToken: "secret"}).Handler()
	unauthorized := httptest.NewRecorder()
	api.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/api/v1/state/export-jobs", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatal("export queue is unprotected")
	}
	requestCtx, abort := context.WithCancel(ctx)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/state/export-jobs", strings.NewReader(`{"sourceIds":["`+id+`"]}`)).WithContext(requestCtx)
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()
	api.ServeHTTP(rec, req)
	abort()
	if rec.Code != http.StatusAccepted {
		t.Fatalf("enqueue %d %s", rec.Code, rec.Body.String())
	}
	var job domain.ExportJob
	if err = json.Unmarshal(rec.Body.Bytes(), &job); err != nil {
		t.Fatal(err)
	}
	claimed, err := store.ClaimExportJob(ctx)
	if err != nil || claimed.ID != job.ID {
		t.Fatalf("claim %+v %v", claimed, err)
	}
	if err = store.UpdateExportJob(ctx, job.ID, "copying", 1); err != nil {
		t.Fatal(err)
	}
	if err = store.RecoverExportJobs(ctx); err != nil {
		t.Fatal(err)
	}
	recovered, err := store.GetExportJob(ctx, job.ID)
	if err != nil || recovered.Status != "QUEUED" || recovered.CompletedSources != 0 {
		t.Fatalf("restart recovery %+v %v", recovered, err)
	}
	workerDone := make(chan struct{})
	go func() { defer close(workerDone); jobs.Run(ctx) }()
	defer func() { cancel(); <-workerDone }()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		job, err = store.GetExportJob(ctx, job.ID)
		if err != nil {
			t.Fatal(err)
		}
		if job.Status == "COMPLETED" {
			break
		}
		if job.Status == "FAILED" {
			t.Fatal(job.ErrorText)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if job.Status != "COMPLETED" || job.Capsule == nil || job.Capsule.ID != job.ID {
		t.Fatalf("job failed to complete: %+v", job)
	}
	httpServer := httptest.NewServer(api)
	defer httpServer.Close()
	agent, err := client.NewAgent(httpServer.URL, "secret", httpServer.Client())
	if err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "result.mstate")
	if err = agent.DownloadExportJob(ctx, job.ID, dest); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(dest); err != nil {
		t.Fatal(err)
	}
	imported, err := client.ImportStateCapsule(ctx, dest, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if imported.Capsule.ID != job.ID {
		t.Fatal("downloaded wrong export")
	}
}
