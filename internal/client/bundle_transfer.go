package client

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/ynw0/airgap-mirror/internal/domain"
	"github.com/ynw0/airgap-mirror/internal/pack"
)

type ValidatedBundle struct {
	Descriptor   domain.BatchDescriptor `json:"descriptor"`
	Root         string                 `json:"root"`
	ManifestPath string                 `json:"manifestPath"`
	PackPaths    map[string]string      `json:"packPaths"`
}

type BundleTransferOptions struct {
	ChunkSize int64  `json:"chunkSize"`
	RequestID string `json:"requestId,omitempty"`
}

type BundleTransferResult struct {
	Descriptor domain.BatchDescriptor `json:"descriptor"`
	Status     ImportStatus           `json:"status"`
}

func manifestMeta(ctx context.Context, db *sql.DB, key string) (string, error) {
	var value string
	if err := db.QueryRowContext(ctx, `SELECT value FROM manifest_meta WHERE key=?`, key).Scan(&value); err != nil {
		return "", err
	}
	return value, nil
}

func ValidateBatchBundle(ctx context.Context, bundleDir string) (ValidatedBundle, error) {
	var out ValidatedBundle
	if bundleDir == "" {
		return out, fmt.Errorf("bundle directory is required: %w", domain.ErrInvalid)
	}
	root, err := filepath.Abs(bundleDir)
	if err != nil {
		return out, err
	}
	info, err := os.Stat(root)
	if err != nil {
		return out, err
	}
	if !info.IsDir() {
		return out, fmt.Errorf("bundle path is not a directory: %w", domain.ErrInvalid)
	}
	descriptor, err := ReadBatchDescriptor(root)
	if err != nil {
		return out, err
	}
	if descriptor.EpochTotalBatches <= 0 || descriptor.BatchSequence <= 0 || descriptor.BatchSequence > descriptor.EpochTotalBatches || descriptor.BatchObjects < 0 || descriptor.BatchBytes < 0 || len(descriptor.Packs) == 0 {
		return out, fmt.Errorf("batch descriptor counters are invalid: %w", domain.ErrInvalid)
	}

	manifestPath := filepath.Join(root, "manifest.sqlite")
	manifestInfo, err := os.Lstat(manifestPath)
	if err != nil {
		return out, err
	}
	if !manifestInfo.Mode().IsRegular() || manifestInfo.Size() != descriptor.ManifestSize {
		return out, fmt.Errorf("manifest file does not match descriptor size/type: %w", domain.ErrConflict)
	}
	manifestSHA, manifestSize, err := pack.FileSHA256(manifestPath)
	if err != nil {
		return out, err
	}
	if manifestSize != descriptor.ManifestSize || !strings.EqualFold(manifestSHA, descriptor.ManifestSHA256) {
		return out, fmt.Errorf("manifest sha256/size differs from batch.json: %w", domain.ErrConflict)
	}

	db, err := sql.Open("sqlite", manifestPath)
	if err != nil {
		return out, err
	}
	defer db.Close()
	for key, expected := range map[string]string{
		"schema_version": strconv.Itoa(domain.ManifestSchemaVersion),
		"source_id":      descriptor.SourceID,
		"epoch_id":       descriptor.EpochID,
		"batch_id":       descriptor.BatchID,
	} {
		got, metaErr := manifestMeta(ctx, db, key)
		if metaErr != nil {
			return out, fmt.Errorf("read manifest meta %s: %w", key, metaErr)
		}
		if got != expected {
			return out, fmt.Errorf("manifest meta %s=%q expected %q: %w", key, got, expected, domain.ErrConflict)
		}
	}

	manifestCounts := map[string]int64{}
	rows, err := db.QueryContext(ctx, `SELECT pack_id,COUNT(*) FROM entries GROUP BY pack_id`)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var packID string
		var count int64
		if err = rows.Scan(&packID, &count); err != nil {
			rows.Close()
			return out, err
		}
		manifestCounts[packID] = count
	}
	if err = rows.Close(); err != nil {
		return out, err
	}
	if err = rows.Err(); err != nil {
		return out, err
	}

	packPaths := make(map[string]string, len(descriptor.Packs))
	seenSequence := make(map[int]struct{}, len(descriptor.Packs))
	seenFile := make(map[string]struct{}, len(descriptor.Packs))
	var declaredEntries int64
	for _, spec := range descriptor.Packs {
		if err = ctx.Err(); err != nil {
			return out, err
		}
		if spec.ID == "" || spec.Sequence <= 0 || spec.Size < pack.HeaderSize || spec.EntryCount < 0 || len(spec.SHA256) != 64 {
			return out, fmt.Errorf("invalid pack declaration %s: %w", spec.ID, domain.ErrInvalid)
		}
		if _, err = strconv.ParseUint(spec.SHA256[:16], 16, 64); err != nil {
			return out, fmt.Errorf("invalid pack sha256 for %s: %w", spec.ID, domain.ErrInvalid)
		}
		if _, exists := packPaths[spec.ID]; exists {
			return out, fmt.Errorf("duplicate pack id %s: %w", spec.ID, domain.ErrConflict)
		}
		if _, exists := seenSequence[spec.Sequence]; exists {
			return out, fmt.Errorf("duplicate pack sequence %d: %w", spec.Sequence, domain.ErrConflict)
		}
		seenSequence[spec.Sequence] = struct{}{}
		expectedFile := filepath.ToSlash(filepath.Join("packs", fmt.Sprintf("%06d.agp", spec.Sequence)))
		if spec.File != expectedFile {
			return out, fmt.Errorf("pack %s file %q expected %q: %w", spec.ID, spec.File, expectedFile, domain.ErrConflict)
		}
		if err = pack.ValidateLogicalPath(spec.File); err != nil {
			return out, err
		}
		if _, exists := seenFile[spec.File]; exists {
			return out, fmt.Errorf("duplicate pack file %s: %w", spec.File, domain.ErrConflict)
		}
		seenFile[spec.File] = struct{}{}
		path := filepath.Join(root, filepath.FromSlash(spec.File))
		packInfo, statErr := os.Lstat(path)
		if statErr != nil {
			return out, statErr
		}
		if !packInfo.Mode().IsRegular() || packInfo.Size() != spec.Size {
			return out, fmt.Errorf("pack %s size/type differs from descriptor: %w", spec.ID, domain.ErrConflict)
		}
		reader, openErr := pack.Open(path)
		if openErr != nil {
			return out, openErr
		}
		header := reader.Header()
		closeErr := reader.Close()
		if closeErr != nil {
			return out, closeErr
		}
		if header.PackID != spec.ID || header.EpochID != descriptor.EpochID || header.BatchID != descriptor.BatchID || int64(header.RecordCount) != spec.EntryCount {
			return out, fmt.Errorf("pack %s header differs from descriptor: %w", spec.ID, domain.ErrConflict)
		}
		if manifestCounts[spec.ID] != spec.EntryCount {
			return out, fmt.Errorf("manifest entries for pack %s=%d expected %d: %w", spec.ID, manifestCounts[spec.ID], spec.EntryCount, domain.ErrConflict)
		}
		delete(manifestCounts, spec.ID)
		declaredEntries += spec.EntryCount
		packPaths[spec.ID] = path
	}
	if len(manifestCounts) != 0 {
		return out, fmt.Errorf("manifest references undeclared packs: %w", domain.ErrConflict)
	}
	if declaredEntries != descriptor.BatchObjects {
		return out, fmt.Errorf("declared pack entries=%d batch objects=%d: %w", declaredEntries, descriptor.BatchObjects, domain.ErrConflict)
	}
	return ValidatedBundle{Descriptor: descriptor, Root: root, ManifestPath: manifestPath, PackPaths: packPaths}, nil
}

func maxPackSize(d domain.BatchDescriptor) int64 {
	var max int64
	for _, p := range d.Packs {
		if p.Size > max {
			max = p.Size
		}
	}
	return max
}

func packStatusByID(status ImportStatus) map[string]domain.ImportPack {
	out := make(map[string]domain.ImportPack, len(status.Packs))
	for _, p := range status.Packs {
		out[p.PackID] = p
	}
	return out
}

func TransferBatchBundle(ctx context.Context, agent *AgentClient, bundleDir string, opts BundleTransferOptions, progress ProgressFunc) (BundleTransferResult, error) {
	var out BundleTransferResult
	if agent == nil {
		return out, fmt.Errorf("agent client is required: %w", domain.ErrInvalid)
	}
	bundle, err := ValidateBatchBundle(ctx, bundleDir)
	if err != nil {
		return out, err
	}
	d := bundle.Descriptor
	out.Descriptor = d
	if err = agent.Health(ctx); err != nil {
		return out, err
	}
	state, err := agent.SourceState(ctx, d.SourceID)
	if err != nil {
		return out, err
	}
	if !state.LiveCursor.Equal(d.BaseCursor) {
		return out, fmt.Errorf("server cursor %v differs from bundle base %v: %w", state.LiveCursor, d.BaseCursor, domain.ErrConflict)
	}
	if state.ActiveEpochID != "" && state.ActiveEpochID != d.EpochID {
		return out, fmt.Errorf("server source has active epoch %s: %w", state.ActiveEpochID, domain.ErrConflict)
	}
	capacity, err := agent.Capacity(ctx, d.SourceID)
	if err != nil {
		return out, err
	}
	need := uint64(d.BatchBytes)
	if max := maxPackSize(d); max > 0 {
		need += uint64(max)
	}
	if capacity.FreeBytes < need {
		return out, fmt.Errorf("server source free=%d required>=%d: %w", capacity.FreeBytes, need, domain.ErrConflict)
	}
	requestID := strings.TrimSpace(opts.RequestID)
	if requestID == "" {
		requestID = "batch-" + d.BatchID
	}
	session, err := agent.CreateImport(ctx, requestID, d)
	if err != nil {
		return out, err
	}
	status, err := agent.ImportStatus(ctx, session.ID)
	if err != nil {
		return out, err
	}
	if status.Session.Status == "BATCH_IMPORTED" {
		out.Status = status
		return out, nil
	}
	if status.Session.Status == "UPLOADING_MANIFEST" {
		if err = agent.UploadManifestVerified(ctx, session.ID, bundle.ManifestPath, d.ManifestSHA256, opts.ChunkSize, progress); err != nil {
			return out, err
		}
		if err = agent.CompleteManifest(ctx, session.ID); err != nil {
			return out, err
		}
		status, err = agent.ImportStatus(ctx, session.ID)
		if err != nil {
			return out, err
		}
	}
	if status.Session.Status != "MANIFEST_READY" && status.Session.Status != "IMPORTING" && status.Session.Status != "BATCH_IMPORTED" {
		return out, fmt.Errorf("import session %s cannot accept packs in status %s: %w", session.ID, status.Session.Status, domain.ErrConflict)
	}
	if status.Session.Status == "BATCH_IMPORTED" {
		out.Status = status
		return out, nil
	}

	specs := append([]domain.PackSpec(nil), d.Packs...)
	sort.Slice(specs, func(i, j int) bool { return specs[i].Sequence < specs[j].Sequence })
	packState := packStatusByID(status)
	for _, spec := range specs {
		if err = ctx.Err(); err != nil {
			return out, err
		}
		remote, ok := packState[spec.ID]
		if !ok {
			return out, fmt.Errorf("server import session lacks pack %s: %w", spec.ID, domain.ErrConflict)
		}
		if remote.ExpectedSize != spec.Size || !strings.EqualFold(remote.ExpectedSHA256, spec.SHA256) {
			return out, fmt.Errorf("server pack declaration differs for %s: %w", spec.ID, domain.ErrConflict)
		}
		switch remote.Status {
		case domain.PackImported:
			continue
		case domain.PackUploading:
			if err = agent.UploadPackVerified(ctx, session.ID, spec.ID, bundle.PackPaths[spec.ID], spec.SHA256, opts.ChunkSize, progress); err != nil {
				return out, err
			}
			if err = agent.CommitPack(ctx, session.ID, spec.ID); err != nil {
				return out, err
			}
		case domain.PackUploaded:
			if err = agent.CommitPack(ctx, session.ID, spec.ID); err != nil {
				return out, err
			}
		default:
			return out, fmt.Errorf("server pack %s is in unsupported resumable status %s: %w", spec.ID, remote.Status, domain.ErrConflict)
		}
		if progress != nil {
			if err = progress(TransferProgress{Kind: "pack-committed", SessionID: session.ID, PackID: spec.ID, Path: bundle.PackPaths[spec.ID], Transferred: spec.Size, Total: spec.Size}); err != nil {
				return out, err
			}
		}
	}
	if err = agent.CompleteBatch(ctx, session.ID); err != nil {
		return out, err
	}
	status, err = agent.ImportStatus(ctx, session.ID)
	if err != nil {
		return out, err
	}
	if status.Session.Status != "BATCH_IMPORTED" {
		return out, fmt.Errorf("server did not complete batch; session status=%s: %w", status.Session.Status, domain.ErrConflict)
	}
	out.Status = status
	return out, nil
}
