package client

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/ynw0/airgap-mirror/internal/domain"
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
