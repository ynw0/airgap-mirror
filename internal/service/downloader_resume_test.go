package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/ynw0/airgap-mirror/internal/domain"
)

type checkpointProgress struct {
	state domain.DownloadEntryProgress
}

func (p *checkpointProgress) EntryState(context.Context, string) (domain.DownloadEntryProgress, error) {
	return p.state, nil
}

func (p *checkpointProgress) MarkEntryState(_ context.Context, state domain.DownloadEntryProgress) error {
	p.state = state
	return nil
}

func TestDownloadArtifactRecoversPartialCheckpoint(t *testing.T) {
	content := []byte("abcdefgh")
	sum := sha256.Sum256(content)
	sha := hex.EncodeToString(sum[:])

	for _, tc := range []struct {
		name          string
		existing      string
		checkpoint    int64
		expectedRange string
	}{
		{name: "uncommitted tail", existing: "abcdXX", checkpoint: 4, expectedRange: "bytes=4-"},
		{name: "uncheckpointed partial", existing: "stale", checkpoint: 0, expectedRange: ""},
		{name: "missing durable prefix", existing: "ab", checkpoint: 4, expectedRange: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotRange := r.Header.Get("Range")
				if gotRange != tc.expectedRange {
					t.Errorf("Range = %q, want %q", gotRange, tc.expectedRange)
				}
				w.Header().Set("ETag", `"v1"`)
				if gotRange != "" {
					w.Header().Set("Content-Range", "bytes 4-7/8")
					w.WriteHeader(http.StatusPartialContent)
					_, _ = w.Write(content[4:])
					return
				}
				_, _ = w.Write(content)
			}))
			defer srv.Close()
			path := filepath.Join(t.TempDir(), "artifact.part")
			if err := os.WriteFile(path, []byte(tc.existing), 0644); err != nil {
				t.Fatal(err)
			}
			progress := &checkpointProgress{state: domain.DownloadEntryProgress{
				EntryID: "artifact", Status: domain.DownloadDownloading,
				Downloaded: tc.checkpoint, ETag: `"v1"`,
			}}
			downloader := Downloader{
				Fetcher: HTTPFetcher{Client: srv.Client()}, Progress: progress, ProgressEvery: 2,
			}
			gotSHA, gotSize, err := downloader.downloadArtifact(context.Background(),
				domain.Artifact{ID: "artifact", UpstreamURL: srv.URL, Size: int64(len(content)), SHA256: sha},
				path, progress.state)
			if err != nil {
				t.Fatal(err)
			}
			if gotSHA != sha || gotSize != int64(len(content)) {
				t.Fatalf("download result sha=%s size=%d", gotSHA, gotSize)
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != string(content) {
				t.Fatalf("partial recovery wrote %q, want %q", got, content)
			}
		})
	}
}
