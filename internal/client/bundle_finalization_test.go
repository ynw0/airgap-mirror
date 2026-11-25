package client

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ynw0/airgap-mirror/internal/domain"
	"github.com/ynw0/airgap-mirror/internal/pack"
	_ "modernc.org/sqlite"
)

func TestFinalizeTransferredBatch(t *testing.T) {
	for _, tc := range []struct {
		name     string
		fail     bool
		status   string
		expected bool
	}{
		{"already imported but publish failed", true, "BATCH_IMPORTED", false},
		{"retry completes publication", false, "BATCH_IMPORTED", true},
		{"server did not complete batch", false, "IMPORTING", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var completions, reads atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodPost && r.URL.Path == "/api/v1/imports/session/complete":
					completions.Add(1)
					if tc.fail {
						http.Error(w, "publication interrupted", http.StatusConflict)
						return
					}
					w.WriteHeader(http.StatusNoContent)
				case r.Method == http.MethodGet && r.URL.Path == "/api/v1/imports/session":
					reads.Add(1)
					_ = json.NewEncoder(w).Encode(ImportStatus{Session: domain.ImportSession{ID: "session", Status: tc.status}})
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			agent, err := NewAgent(server.URL, "", server.Client())
			if err != nil {
				t.Fatal(err)
			}
			status, err := finalizeTransferredBatch(context.Background(), agent, "session")
			if (err == nil) != tc.expected {
				t.Fatalf("unexpected finalization result: %+v, %v", status, err)
			}
			if completions.Load() != 1 || (tc.fail && reads.Load() != 0) || (!tc.fail && reads.Load() != 1) {
				t.Fatalf("unexpected calls: complete=%d read=%d", completions.Load(), reads.Load())
			}
			if !tc.expected && !errors.Is(err, domain.ErrConflict) {
				t.Fatalf("expected conflict: %v", err)
			}
		})
	}
}

func TestResumeReportsCommittedPacksAndInstallation(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "packs"), 0755); err != nil {
		t.Fatal(err)
	}
	newID := func() string {
		id, err := domain.NewID()
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	d := domain.BatchDescriptor{
		SchemaVersion: domain.BundleSchemaVersion, SourceID: newID(), EpochID: newID(), BatchID: newID(),
		EpochTotalBatches: 1, BatchSequence: 1,
	}
	manifest := filepath.Join(root, "manifest.sqlite")
	db, err := sql.Open("sqlite", manifest)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = pack.InitManifest(ctx, db); err != nil {
		t.Fatal(err)
	}
	for key, value := range map[string]string{"schema_version": strconv.Itoa(domain.ManifestSchemaVersion), "source_id": d.SourceID, "epoch_id": d.EpochID, "batch_id": d.BatchID} {
		if err = pack.PutManifestMeta(ctx, db, key, value); err != nil {
			t.Fatal(err)
		}
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	d.ManifestSHA256, d.ManifestSize, err = pack.FileSHA256(manifest)
	if err != nil {
		t.Fatal(err)
	}
	session := domain.ImportSession{ID: "session", SourceID: d.SourceID, EpochID: d.EpochID, BatchID: d.BatchID, ManifestSize: d.ManifestSize, ManifestSHA256: d.ManifestSHA256, Status: "IMPORTING"}
	status := ImportStatus{Session: session}
	for i, state := range []domain.PackStatus{domain.PackImported, domain.PackUploaded} {
		id := newID()
		file := fmt.Sprintf("packs/%06d.agp", i+1)
		path := filepath.Join(root, filepath.FromSlash(file))
		writer, err := pack.Create(path, domain.PackHeader{PackID: id, EpochID: d.EpochID, BatchID: d.BatchID})
		if err != nil {
			t.Fatal(err)
		}
		if err = writer.Close(); err != nil {
			t.Fatal(err)
		}
		sum, size, err := pack.FileSHA256(path)
		if err != nil {
			t.Fatal(err)
		}
		d.Packs = append(d.Packs, domain.PackSpec{ID: id, Sequence: i + 1, File: file, Size: size, SHA256: sum})
		status.Packs = append(status.Packs, domain.ImportPack{SessionID: session.ID, PackID: id, ExpectedSize: size, ExpectedSHA256: sum, UploadedSize: size, Status: state})
	}
	body, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, "batch.json"), body, 0644); err != nil {
		t.Fatal(err)
	}
	var committed, completed atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /api/v1/health":
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
		case "GET /api/v1/sources/" + d.SourceID + "/state":
			_ = json.NewEncoder(w).Encode(domain.SourceState{SourceID: d.SourceID, ActiveEpochID: d.EpochID})
		case "POST /api/v1/imports":
			_ = json.NewEncoder(w).Encode(session)
		case "GET /api/v1/imports/session":
			result := status
			if completed.Load() {
				result.Session.Status = "BATCH_IMPORTED"
			}
			_ = json.NewEncoder(w).Encode(result)
		case "POST /api/v1/imports/session/packs/" + d.Packs[1].ID + "/commit":
			committed.Store(true)
			w.WriteHeader(http.StatusNoContent)
		case "POST /api/v1/imports/session/complete":
			completed.Store(true)
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	agent, err := NewAgent(server.URL, "", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	var events []TransferProgress
	_, err = TransferBatchBundle(ctx, agent, root, BundleTransferOptions{}, func(p TransferProgress) error {
		events = append(events, p)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, p := range events {
		kinds = append(kinds, p.Kind)
	}
	if got := strings.Join(kinds, ","); got != "manifest-ready,pack-committed,pack-installing,pack-committed,finalizing" {
		t.Fatalf("unexpected resumed progress: %s", got)
	}
	if events[1].PackID != d.Packs[0].ID || events[1].Transferred != d.Packs[0].Size || !committed.Load() || !completed.Load() {
		t.Fatalf("resume did not report and finish the batch: %+v", events)
	}
}
