package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ynw0/airgap-mirror/internal/ports"
)

func TestHTTPFetcherRangeResumeRequires206(t *testing.T) {
	var gotRange, gotIfRange string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotRange = r.Header.Get("Range")
		gotIfRange = r.Header.Get("If-Range")
		http.Error(w, "full body", http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	_, err := (HTTPFetcher{Client: srv.Client()}).Open(context.Background(), ports.FetchRequest{
		URL:    srv.URL,
		Offset: 10,
		ETag:   `"abc"`,
	})
	if err == nil || !strings.Contains(err.Error(), "upstream ignored range resume") {
		t.Fatalf("expected range-resume failure, got %v", err)
	}
	if gotRange != "bytes=10-" || gotIfRange != `"abc"` {
		t.Fatalf("Range=%q If-Range=%q", gotRange, gotIfRange)
	}
}

func TestHTTPFetcherAcceptsPartialContent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Range", "bytes 4-8/9")
		w.Header().Set("ETag", `"v1"`)
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write([]byte("partial"))
	}))
	t.Cleanup(srv.Close)

	resp, err := (HTTPFetcher{Client: srv.Client()}).Open(context.Background(), ports.FetchRequest{
		URL:    srv.URL,
		Offset: 4,
		ETag:   `"v1"`,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent || !resp.AcceptRanges {
		t.Fatalf("status=%d acceptRanges=%v", resp.StatusCode, resp.AcceptRanges)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "partial" {
		t.Fatalf("body=%q", body)
	}
}

func TestHTTPFetcherRejectsWrongContentRange(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Range", "bytes 0-8/9")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write([]byte("nope"))
	}))
	t.Cleanup(srv.Close)

	_, err := (HTTPFetcher{Client: srv.Client()}).Open(context.Background(), ports.FetchRequest{URL: srv.URL, Offset: 4})
	if err == nil || !strings.Contains(err.Error(), "unexpected content-range") {
		t.Fatalf("expected content-range mismatch, got %v", err)
	}
}
