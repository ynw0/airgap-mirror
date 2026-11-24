package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/ynw0/airgap-mirror/internal/domain"
	"github.com/ynw0/airgap-mirror/internal/pack"
)

type approvedRepairStore interface {
	HasApprovedRepair(context.Context, string, string) (bool, error)
	ApprovedRepairEvidence(context.Context, string, string, string, int64, string) (domain.RepairReplacementEvidence, error)
}

// Serialize approved replacements in this Agent. Normal imports never overwrite
// an existing immutable package. External repository writers must stay stopped.
var verifiedRepairMu sync.Mutex

func sameRepairFile(a, b os.FileInfo) bool {
	return a.Mode().IsRegular() && b.Mode().IsRegular() && os.SameFile(a, b) && a.Size() == b.Size() && a.ModTime().Equal(b.ModTime())
}

func syncRepairDirectory(path string) error {
	d, err := os.Open(path)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// repairVerifiedPackage accepts only a payload whose expected identity matches
// server-side approval and whose old file still matches the integrity report.
// The original inode is hard-linked into a private quarantine before the
// verified same-directory temporary file replaces the public path atomically.
func repairVerifiedPackage(ctx context.Context, source domain.Source, rec domain.PackRecord, body io.Reader, evidence domain.RepairReplacementEvidence) (domain.InstallResult, error) {
	var out domain.InstallResult
	if source.Type != domain.SourcePyPI || source.Provider != "pypi" || !strings.HasPrefix(rec.LogicalPath, "packages/") ||
		pack.IsStagingLogical(rec.LogicalPath) || rec.LogicalPath != evidence.LogicalPath || rec.ContentLength != evidence.Size ||
		rec.SHA256 != evidence.SHA256 || rec.ContentLength < 0 || rec.ContentLength == math.MaxInt64 ||
		evidence.ActualSize < 0 || len(evidence.ActualSHA256) != 64 || len(rec.SHA256) != 64 ||
		evidence.JobID == "" || strings.ContainsAny(evidence.JobID, "/\\") || evidence.JobID == "." || evidence.JobID == ".." {
		return out, fmt.Errorf("invalid approved package repair: %w", domain.ErrConflict)
	}
	if _, err := hex.DecodeString(evidence.ActualSHA256); err != nil {
		return out, domain.ErrInvalid
	}
	if _, err := hex.DecodeString(rec.SHA256); err != nil {
		return out, domain.ErrInvalid
	}
	verifiedRepairMu.Lock()
	defer verifiedRepairMu.Unlock()
	if err := ctx.Err(); err != nil {
		return out, err
	}
	target, err := safeTarget(source.RootPath, rec.LogicalPath)
	if err != nil {
		return out, err
	}
	old, info, err := OpenPublishedFile(source.RootPath, rec.LogicalPath)
	if errors.Is(err, os.ErrNotExist) {
		return (FileInstaller{}).Install(ctx, source, rec, body)
	}
	if err != nil {
		return out, err
	}
	defer old.Close()
	h := sha256.New()
	n, err := io.Copy(h, exportContextReader{ctx: ctx, r: old})
	if err != nil {
		return out, err
	}
	oldSum := hex.EncodeToString(h.Sum(nil))
	after, err := old.Stat()
	if err != nil {
		return out, err
	}
	if n != info.Size() || !sameRepairFile(info, after) {
		return out, fmt.Errorf("原软件包在校验期间变化: %w", domain.ErrConflict)
	}
	current, err := os.Lstat(target)
	if err != nil {
		return out, err
	}
	if !sameRepairFile(info, current) {
		return out, domain.ErrConflict
	}
	out = domain.InstallResult{LogicalPath: rec.LogicalPath, Bytes: rec.ContentLength}
	if n == rec.ContentLength && oldSum == rec.SHA256 {
		// A prior attempt might have completed the rename before the DB commit.
		if err = chmodPublished(source.RootPath, target); err != nil {
			return out, err
		}
		if err = syncRepairDirectory(filepath.Dir(target)); err != nil {
			return out, err
		}
		out.Skipped = true
		return out, nil
	}
	if n != evidence.ActualSize || oldSum != evidence.ActualSHA256 {
		return out, fmt.Errorf("损坏文件已不同于授权报告，请重新校验后授权: %w", domain.ErrConflict)
	}
	tmp, err := os.CreateTemp(filepath.Dir(target), ".airgap-repair-*")
	if err != nil {
		return out, err
	}
	defer func() { _ = tmp.Close(); _ = os.Remove(tmp.Name()) }()
	h.Reset()
	n, err = io.Copy(io.MultiWriter(tmp, h), io.LimitReader(exportContextReader{ctx: ctx, r: body}, rec.ContentLength+1))
	if err != nil {
		return out, err
	}
	if n != rec.ContentLength || hex.EncodeToString(h.Sum(nil)) != rec.SHA256 {
		return out, fmt.Errorf("修复文件大小或 SHA256 与正式账本不一致: %w", domain.ErrConflict)
	}
	if err = tmp.Chmod(publishedFileMode); err != nil {
		return out, err
	}
	if err = tmp.Sync(); err != nil {
		return out, err
	}
	if err = tmp.Close(); err != nil {
		return out, err
	}
	if err = ctx.Err(); err != nil {
		return out, err
	}
	if err = rejectSymlinkParents(source.RootPath, target); err != nil {
		return out, err
	}
	current, err = os.Lstat(target)
	if err != nil {
		return out, err
	}
	if !sameRepairFile(info, current) {
		return out, fmt.Errorf("原软件包在修复期间变化: %w", domain.ErrConflict)
	}

	id, err := domain.NewID()
	if err != nil {
		return out, err
	}
	quarantine, err := safeTarget(source.RootPath, ".airgap-staging/repair-quarantine")
	if err != nil {
		return out, err
	}
	backup, err := safeTarget(source.RootPath, ".airgap-staging/repair-quarantine/"+evidence.JobID+"/"+id+"/"+rec.LogicalPath)
	if err != nil {
		return out, err
	}
	if err = rejectSymlinkParents(source.RootPath, backup); err != nil {
		return out, err
	}
	if err = os.MkdirAll(filepath.Dir(backup), 0700); err != nil {
		return out, err
	}
	if err = rejectSymlinkParents(source.RootPath, backup); err != nil {
		return out, err
	}
	if err = os.Chmod(quarantine, 0700); err != nil {
		return out, err
	}
	// A hard-link is intentionally required. Cross-device quarantine or an
	// unsupported filesystem fails without replacing the original file.
	if err = os.Link(target, backup); err != nil {
		return out, fmt.Errorf("保存原损坏文件失败: %w", err)
	}
	backupInfo, err := os.Lstat(backup)
	if err != nil {
		return out, err
	}
	if !sameRepairFile(info, backupInfo) {
		return out, domain.ErrConflict
	}
	// Persist every directory from the backup leaf up to the repository root.
	root, err := filepath.Abs(source.RootPath)
	if err != nil {
		return out, err
	}
	for dir := filepath.Dir(backup); ; dir = filepath.Dir(dir) {
		if err = syncRepairDirectory(dir); err != nil {
			return out, err
		}
		if dir == root {
			break
		}
	}
	if err = ctx.Err(); err != nil {
		return out, err
	}
	current, err = os.Lstat(target)
	if err != nil {
		return out, err
	}
	if !sameRepairFile(info, current) {
		return out, fmt.Errorf("原软件包在修复期间变化: %w", domain.ErrConflict)
	}
	if err = os.Rename(tmp.Name(), target); err != nil {
		return out, err
	}
	if err = syncRepairDirectory(filepath.Dir(target)); err != nil {
		return out, err
	}
	return out, nil
}
