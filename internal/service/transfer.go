package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/ynw0/airgap-mirror/internal/domain"
	"github.com/ynw0/airgap-mirror/internal/pack"
	"github.com/ynw0/airgap-mirror/internal/ports"
)

const MaxUploadChunk = int64(128 << 20)

type BundleRegistrar struct {
	Store       ports.ServerStore
	Capacity    ports.CapacityInspector
	StagingRoot string
	Now         func() time.Time
}

func (r BundleRegistrar) Register(ctx context.Context, requestID string, d domain.BatchDescriptor) (domain.ImportSession, error) {
	if r.Store == nil || r.StagingRoot == "" || requestID == "" {
		return domain.ImportSession{}, fmt.Errorf("registrar is not configured: %w", domain.ErrInvalid)
	}
	if d.SchemaVersion != domain.BundleSchemaVersion || d.ManifestSize < 0 || len(d.ManifestSHA256) != 64 {
		return domain.ImportSession{}, domain.ErrInvalid
	}
	seen := map[string]struct{}{}
	var maxPack int64
	for _, p := range d.Packs {
		if p.ID == "" || p.Size < 0 || len(p.SHA256) != 64 {
			return domain.ImportSession{}, fmt.Errorf("invalid pack declaration: %w", domain.ErrInvalid)
		}
		if _, ok := seen[p.ID]; ok {
			return domain.ImportSession{}, fmt.Errorf("duplicate pack %s: %w", p.ID, domain.ErrInvalid)
		}
		seen[p.ID] = struct{}{}
		if p.Size > maxPack {
			maxPack = p.Size
		}
	}
	source, err := r.Store.GetSource(ctx, d.SourceID)
	if err != nil {
		return domain.ImportSession{}, err
	}
	if r.Capacity != nil {
		cap, err := r.Capacity.Inspect(ctx, source.RootPath)
		if err != nil {
			return domain.ImportSession{}, err
		}
		need := uint64(d.BatchBytes)
		if maxPack > 0 {
			need += uint64(maxPack)
		}
		if cap.FreeBytes < need {
			return domain.ImportSession{}, fmt.Errorf("source filesystem free=%d required>=%d: %w", cap.FreeBytes, need, domain.ErrConflict)
		}
		if err = os.MkdirAll(r.StagingRoot, 0755); err != nil {
			return domain.ImportSession{}, err
		}
		sc, err := r.Capacity.Inspect(ctx, r.StagingRoot)
		if err != nil {
			return domain.ImportSession{}, err
		}
		stageNeed := uint64(maxPack + d.ManifestSize)
		if sc.FreeBytes < stageNeed {
			return domain.ImportSession{}, fmt.Errorf("staging filesystem free=%d required>=%d: %w", sc.FreeBytes, stageNeed, domain.ErrConflict)
		}
	}
	id, err := domain.NewID()
	if err != nil {
		return domain.ImportSession{}, err
	}
	now := time.Now()
	if r.Now != nil {
		now = r.Now()
	}
	root := filepath.Join(r.StagingRoot, id)
	if err = os.MkdirAll(filepath.Join(root, "packs"), 0755); err != nil {
		return domain.ImportSession{}, err
	}
	s := domain.ImportSession{ID: id, RequestID: requestID, SourceID: d.SourceID, EpochID: d.EpochID, BatchID: d.BatchID, ManifestPath: filepath.Join(root, "manifest.sqlite"), ManifestSize: d.ManifestSize, ManifestSHA256: d.ManifestSHA256, Status: "UPLOADING_MANIFEST", StagingPath: root, CreatedAt: now, UpdatedAt: now}
	ips := make([]domain.ImportPack, 0, len(d.Packs))
	for _, p := range d.Packs {
		ips = append(ips, domain.ImportPack{SessionID: id, PackID: p.ID, ExpectedSize: p.Size, ExpectedSHA256: p.SHA256, StagingPath: filepath.Join(root, "packs", p.ID+".agp"), Status: domain.PackUploading})
	}
	out, err := r.Store.RegisterImportBundle(ctx, d, s, ips)
	if err != nil {
		_ = os.RemoveAll(root)
		return domain.ImportSession{}, err
	}
	if out.ID != id {
		_ = os.RemoveAll(root)
	}
	return out, nil
}

type TransferService struct {
	Store ports.ServerStore
	Now   func() time.Time
}

func (t TransferService) now() time.Time {
	if t.Now != nil {
		return t.Now()
	}
	return time.Now()
}
func fileOffset(path string) (int64, error) {
	st, err := os.Stat(path)
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return st.Size(), nil
}
func appendChunk(path string, offset, total int64, r io.Reader, n int64) (int64, error) {
	if offset < 0 || n < 0 || n > MaxUploadChunk {
		return offset, domain.ErrInvalid
	}
	actual, err := fileOffset(path)
	if err != nil {
		return offset, err
	}
	if actual != offset {
		return actual, fmt.Errorf("upload offset %d != server file size %d: %w", offset, actual, domain.ErrConflict)
	}
	if offset+n > total {
		return offset, fmt.Errorf("chunk exceeds expected size: %w", domain.ErrInvalid)
	}
	if err = os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return offset, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return offset, err
	}
	defer f.Close()
	if _, err = f.Seek(offset, io.SeekStart); err != nil {
		return offset, err
	}
	written, err := io.CopyN(f, r, n)
	if err != nil {
		return offset + written, err
	}
	if err = f.Sync(); err != nil {
		return offset + written, err
	}
	return offset + written, nil
}
func (t TransferService) ManifestOffset(ctx context.Context, id string) (int64, error) {
	s, err := t.Store.GetImportSession(ctx, id)
	if err != nil {
		return 0, err
	}
	actual, err := fileOffset(s.ManifestPath)
	if err != nil {
		return 0, err
	}
	if actual < s.ManifestUploaded {
		return actual, fmt.Errorf("manifest file=%d is behind committed database offset=%d: %w", actual, s.ManifestUploaded, domain.ErrConflict)
	}
	if actual > s.ManifestUploaded {
		if actual > s.ManifestSize {
			return actual, fmt.Errorf("manifest file exceeds expected size: %w", domain.ErrConflict)
		}
		if err = os.Truncate(s.ManifestPath, s.ManifestUploaded); err != nil {
			return actual, err
		}
	}
	return s.ManifestUploaded, nil
}
func (t TransferService) AppendManifest(ctx context.Context, id string, offset int64, r io.Reader, n int64) (int64, error) {
	s, err := t.Store.GetImportSession(ctx, id)
	if err != nil {
		return 0, err
	}
	if s.Status != "UPLOADING_MANIFEST" {
		return s.ManifestUploaded, fmt.Errorf("manifest not uploadable in status %s: %w", s.Status, domain.ErrConflict)
	}
	next, err := appendChunk(s.ManifestPath, offset, s.ManifestSize, r, n)
	if err != nil {
		return next, err
	}
	s.ManifestUploaded = next
	s.UpdatedAt = t.now()
	if err = t.Store.UpdateImportSession(ctx, s); err != nil {
		return next, err
	}
	return next, nil
}
func (t TransferService) CompleteManifest(ctx context.Context, id string) error {
	s, err := t.Store.GetImportSession(ctx, id)
	if err != nil {
		return err
	}
	h, n, err := pack.FileSHA256(s.ManifestPath)
	if err != nil {
		return err
	}
	if n != s.ManifestSize || h != s.ManifestSHA256 {
		return fmt.Errorf("manifest integrity mismatch size=%d/%d sha=%s/%s: %w", n, s.ManifestSize, h, s.ManifestSHA256, domain.ErrConflict)
	}
	if err = t.Store.RegisterManifest(ctx, id); err != nil {
		return err
	}
	return nil
}
func (t TransferService) PackOffset(ctx context.Context, sid, pid string) (int64, error) {
	p, err := t.Store.GetImportPack(ctx, sid, pid)
	if err != nil {
		return 0, err
	}
	actual, err := fileOffset(p.StagingPath)
	if err != nil {
		return 0, err
	}
	if actual < p.UploadedSize {
		return actual, fmt.Errorf("pack file=%d is behind committed database offset=%d: %w", actual, p.UploadedSize, domain.ErrConflict)
	}
	if actual > p.UploadedSize {
		if actual > p.ExpectedSize {
			return actual, fmt.Errorf("pack file exceeds expected size: %w", domain.ErrConflict)
		}
		if err = os.Truncate(p.StagingPath, p.UploadedSize); err != nil {
			return actual, err
		}
	}
	return p.UploadedSize, nil
}
func (t TransferService) AppendPack(ctx context.Context, sid, pid string, offset int64, r io.Reader, n int64) (int64, error) {
	p, err := t.Store.GetImportPack(ctx, sid, pid)
	if err != nil {
		return 0, err
	}
	if p.Status != domain.PackUploading {
		return p.UploadedSize, fmt.Errorf("pack not uploadable in status %s: %w", p.Status, domain.ErrConflict)
	}
	next, err := appendChunk(p.StagingPath, offset, p.ExpectedSize, r, n)
	if err != nil {
		return next, err
	}
	p.UploadedSize = next
	if next == p.ExpectedSize {
		p.Status = domain.PackUploaded
	}
	if err = t.Store.UpdateImportPack(ctx, p); err != nil {
		return next, err
	}
	return next, nil
}
func verifyUploadedPack(p domain.ImportPack) error {
	h, n, err := pack.FileSHA256(p.StagingPath)
	if err != nil {
		return err
	}
	if n != p.ExpectedSize || h != p.ExpectedSHA256 {
		return fmt.Errorf("pack integrity mismatch size=%d/%d sha=%s/%s: %w", n, p.ExpectedSize, h, p.ExpectedSHA256, domain.ErrConflict)
	}
	return nil
}

func sha256Hex(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
