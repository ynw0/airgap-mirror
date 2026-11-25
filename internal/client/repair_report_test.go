package client

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ynw0/airgap-mirror/internal/domain"
)

func TestWriteRepairReport(t *testing.T) {
	for _, tc := range []struct {
		name  string
		total int64
		mode  string
	}{
		{"multiple pages", 3, "ok"},
		{"empty report", 0, "empty"},
		{"incomplete count", 4, "ok"},
		{"repeated cursor", 3, "repeat"},
		{"server failure", 3, "failure"},
		{"unfinished job", 3, "unfinished"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer test-token" {
					t.Error("missing authorization")
				}
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/api/v1/maintenance/job":
					status := domain.MaintenanceCompleted
					if tc.mode == "unfinished" {
						status = domain.MaintenanceRunning
					}
					_ = json.NewEncoder(w).Encode(domain.MaintenanceJob{ID: "job", SourceID: "source", Kind: domain.MaintenanceRepair, Status: status, CandidateObjects: tc.total})
				case "/api/v1/maintenance/job/repair-issues":
					if r.URL.Query().Get("limit") != "500" {
						t.Error("unexpected page size")
					}
					switch {
					case tc.mode == "empty":
						_ = json.NewEncoder(w).Encode(RepairIssuesPage{})
					case r.URL.Query().Get("after") == "":
						_ = json.NewEncoder(w).Encode(RepairIssuesPage{Items: []domain.RepairIssue{{LogicalPath: "a"}, {LogicalPath: "b"}}, Next: "b"})
					case tc.mode == "failure":
						http.Error(w, "failed", http.StatusInternalServerError)
					case tc.mode == "repeat":
						_ = json.NewEncoder(w).Encode(RepairIssuesPage{Items: []domain.RepairIssue{{LogicalPath: "b"}}, Next: "b"})
					default:
						_ = json.NewEncoder(w).Encode(RepairIssuesPage{Items: []domain.RepairIssue{{LogicalPath: "c"}}})
					}
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			agent, err := NewAgent(server.URL, "test-token", server.Client())
			if err != nil {
				t.Fatal(err)
			}
			var buf bytes.Buffer
			count, err := agent.WriteRepairReport(context.Background(), "job", &buf, nil)
			wantSuccess := tc.mode == "empty" || (tc.mode == "ok" && tc.total == 3)
			if (err == nil) != wantSuccess {
				t.Fatalf("success=%v, want %v, error=%v", err == nil, wantSuccess, err)
			}
			if !wantSuccess {
				return
			}
			var report struct {
				Scope string               `json:"scope"`
				Total int64                `json:"total"`
				Items []domain.RepairIssue `json:"items"`
			}
			if err = json.Unmarshal(buf.Bytes(), &report); err != nil {
				t.Fatal(err)
			}
			if count != tc.total || report.Total != tc.total || int64(len(report.Items)) != tc.total || report.Scope != "complete" || strings.Contains(buf.String(), "test-token") {
				t.Fatalf("unexpected report: %s", buf.String())
			}
		})
	}
}
