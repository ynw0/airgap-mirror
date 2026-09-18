package pack

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/ynw0/airgap-mirror/internal/domain"
)

func mustID(t *testing.T) string {
	t.Helper()
	id, err := domain.NewID()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func shaHex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func TestPackWriteReadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "000001.agp")
	header := domain.PackHeader{PackID: mustID(t), EpochID: mustID(t), BatchID: mustID(t)}
	w, err := Create(path, header)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	body := []byte("artifact-bytes")
	art := domain.Artifact{
		ID:          mustID(t),
		LogicalPath: "pool/main/a.deb",
		Size:        int64(len(body)),
		SHA256:      shaHex(body),
	}
	loc, err := w.Append(context.Background(), domain.PackEntry{Artifact: art}, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if loc.Offset != PackHeaderSize {
		t.Fatalf("first record offset=%d", loc.Offset)
	}
	if err = w.Close(); err != nil {
		t.Fatal(err)
	}

	r, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	h := r.Header()
	if h.PackID != header.PackID || h.EpochID != header.EpochID || h.BatchID != header.BatchID {
		t.Fatalf("header identity mismatch: %+v", h)
	}
	if h.RecordCount != 1 {
		t.Fatalf("record count=%d", h.RecordCount)
	}
	rec, reader, err := r.Next(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if rec.EntryID != art.ID || rec.LogicalPath != art.LogicalPath || rec.ContentLength != art.Size || rec.SHA256 != art.SHA256 {
		t.Fatalf("record mismatch: %+v", rec)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("payload mismatch")
	}
	if _, _, err = r.Next(context.Background()); err != io.EOF {
		t.Fatalf("expected EOF, got %v", err)
	}
}

func TestPackResumeTruncatesIncompleteTail(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "000001.agp")
	header := domain.PackHeader{PackID: mustID(t), EpochID: mustID(t), BatchID: mustID(t)}
	w, err := Create(path, header)
	if err != nil {
		t.Fatal(err)
	}
	body := []byte("complete-record")
	art := domain.Artifact{
		ID:          mustID(t),
		LogicalPath: "ok.bin",
		Size:        int64(len(body)),
		SHA256:      shaHex(body),
	}
	if _, err = w.Append(context.Background(), domain.PackEntry{Artifact: art}, bytes.NewReader(body)); err != nil {
		t.Fatal(err)
	}
	if err = w.Close(); err != nil {
		t.Fatal(err)
	}

	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.Write([]byte("AGMREC01-incomplete-tail")); err != nil {
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}

	rw, err := Resume(path, header)
	if err != nil {
		t.Fatal(err)
	}
	defer rw.Close()
	second := []byte("second")
	art2 := domain.Artifact{
		ID:          mustID(t),
		LogicalPath: "two.bin",
		Size:        int64(len(second)),
		SHA256:      shaHex(second),
	}
	if _, err = rw.Append(context.Background(), domain.PackEntry{Artifact: art2}, bytes.NewReader(second)); err != nil {
		t.Fatal(err)
	}
	if err = rw.Close(); err != nil {
		t.Fatal(err)
	}

	r, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if r.Header().RecordCount != 2 {
		t.Fatalf("after resume record count=%d", r.Header().RecordCount)
	}
	rec1, _, err := r.Next(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if rec1.EntryID != art.ID {
		t.Fatalf("first record lost: %s", rec1.EntryID)
	}
	rec2, _, err := r.Next(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if rec2.EntryID != art2.ID {
		t.Fatalf("second record missing: %s", rec2.EntryID)
	}
}

func TestPackResumeRejectsIdentityMismatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "000001.agp")
	header := domain.PackHeader{PackID: mustID(t), EpochID: mustID(t), BatchID: mustID(t)}
	w, err := Create(path, header)
	if err != nil {
		t.Fatal(err)
	}
	if err = w.Close(); err != nil {
		t.Fatal(err)
	}
	wrong := header
	wrong.BatchID = mustID(t)
	if _, err = Resume(path, wrong); err == nil {
		t.Fatal("resume should reject mismatched batch identity")
	}
}

func TestPackAppendRejectsUnsafePath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "000001.agp")
	w, err := Create(path, domain.PackHeader{PackID: mustID(t), EpochID: mustID(t), BatchID: mustID(t)})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	body := []byte("x")
	art := domain.Artifact{ID: mustID(t), LogicalPath: "../escape", Size: 1, SHA256: shaHex(body)}
	if _, err = w.Append(context.Background(), domain.PackEntry{Artifact: art}, bytes.NewReader(body)); err == nil {
		t.Fatal("unsafe path should be rejected")
	}
}
