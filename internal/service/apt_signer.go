package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/ynw0/airgap-mirror/internal/adapters/apt"
	"github.com/ynw0/airgap-mirror/internal/domain"
	"github.com/ynw0/airgap-mirror/internal/ports"
)

var aptSigningSuite = regexp.MustCompile("^[A-Za-z0-9._-]+$")
var aptKeyFingerprint = regexp.MustCompile("^[A-Fa-f0-9]{40,64}$")

type APTSigner struct {
	Sources   ports.ServerStore
	Catalog   ports.CatalogStore
	Jobs      ports.MaintenanceStore
	GNUPGHOME string
	KeyID     string
	mu        sync.Mutex
}

type APTSignResult struct {
	SourceID string   `json:"sourceId"`
	Suite    string   `json:"suite"`
	Files    []string `json:"files"`
}

func (s *APTSigner) configured() error {
	if s == nil || s.Sources == nil || s.Catalog == nil || s.Jobs == nil {
		return fmt.Errorf("APT signer is not configured: %w", domain.ErrInvalid)
	}
	if !filepath.IsAbs(s.GNUPGHOME) || !aptKeyFingerprint.MatchString(s.KeyID) {
		return fmt.Errorf("请先运行 configure-apt-signing.sh 配置专用 GPG 密钥: %w", domain.ErrInvalid)
	}
	return nil
}

func (s *APTSigner) gpg(ctx context.Context, args ...string) ([]byte, error) {
	all := append([]string{"--homedir", s.GNUPGHOME, "--batch", "--no-tty"}, args...)
	cmd := exec.CommandContext(ctx, "gpg", all...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("gpg 运行失败: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return out, nil
}

func (s *APTSigner) ExportPublicKey(ctx context.Context) ([]byte, error) {
	if err := s.configured(); err != nil {
		return nil, err
	}
	out, err := s.gpg(ctx, "--armor", "--export", s.KeyID)
	if err != nil {
		return nil, err
	}
	if len(out) == 0 || len(out) > 1<<20 {
		return nil, fmt.Errorf("签名公钥为空或大小异常: %w", domain.ErrConflict)
	}
	return out, nil
}

func (s *APTSigner) Sign(ctx context.Context, sourceID string) (APTSignResult, error) {
	var result APTSignResult
	if err := s.configured(); err != nil {
		return result, err
	}
	// 签名和文件账本必须按顺序更新，避免两个签名请求互相覆盖。
	s.mu.Lock()
	defer s.mu.Unlock()

	source, err := s.Sources.GetSource(ctx, sourceID)
	if err != nil {
		return result, err
	}
	if source.Type != domain.SourceAPT || source.Provider != apt.Provider {
		return result, fmt.Errorf("只有 APT 软件源支持重新签名: %w", domain.ErrInvalid)
	}
	var cfg struct {
		Suite       string `json:"suite"`
		ReleaseMode string `json:"releaseMode"`
	}
	if err = json.Unmarshal(source.ConfigJSON, &cfg); err != nil {
		return result, err
	}
	if !aptSigningSuite.MatchString(cfg.Suite) {
		return result, fmt.Errorf("APT 发行版代号无效: %w", domain.ErrInvalid)
	}
	state, err := s.Sources.GetState(ctx, sourceID)
	if err != nil {
		return result, err
	}
	if state.ActiveEpochID != "" {
		return result, fmt.Errorf("软件源正在同步，请先完成所有批次导入: %w", domain.ErrConflict)
	}

	jobs, err := s.Jobs.ListMaintenanceJobs(ctx, sourceID, 100)
	if err != nil {
		return result, err
	}
	var latest *domain.MaintenanceJob
	for index := range jobs {
		job := &jobs[index]
		if job.Status == domain.MaintenanceQueued || job.Status == domain.MaintenanceRunning {
			return result, fmt.Errorf("文件检查任务还没有完成: %w", domain.ErrConflict)
		}
		if job.Kind == domain.MaintenanceInventory && (latest == nil || job.CreatedAt.After(latest.CreatedAt)) {
			latest = job
		}
	}
	if latest == nil || latest.Status != domain.MaintenanceCompleted || latest.CompletedAt == nil ||
		latest.CompletedAt.Before(state.UpdatedAt) || latest.ScannedObjects != state.LiveObjects || latest.ScannedBytes != state.LiveBytes {
		return result, fmt.Errorf("请先重新清点文件并确认统计结果，然后再签名: %w", domain.ErrConflict)
	}

	// 使用当前仓库的 Release 正文，不在服务器上重新生成依赖索引。
	inputName := "Release"
	if cfg.ReleaseMode == "inrelease" {
		inputName = "InRelease"
	}
	base := filepath.ToSlash(filepath.Join("dists", cfg.Suite))
	logical := base + "/" + inputName
	file, err := safeTarget(source.RootPath, logical)
	if err != nil {
		return result, err
	}
	if err = rejectSymlinkParents(source.RootPath, file); err != nil {
		return result, err
	}
	info, err := os.Lstat(file)
	if err != nil {
		return result, err
	}
	if !info.Mode().IsRegular() || info.Size() > 32<<20 {
		return result, fmt.Errorf("Release 文件不是普通文件或文件过大: %w", domain.ErrConflict)
	}
	currentSHA, currentSize, err := hashFile(file)
	if err != nil {
		return result, err
	}
	recorded, err := s.Catalog.Get(ctx, sourceID, logical)
	if err != nil {
		return result, fmt.Errorf("仓库账本没有 Release 文件，请重新清点: %w", err)
	}
	if currentSHA != recorded.SHA256 || currentSize != recorded.Size {
		return result, fmt.Errorf("Release 文件与清点结果不同，请先重新清点: %w", domain.ErrConflict)
	}
	plain, err := os.ReadFile(file)
	if err != nil {
		return result, err
	}
	if inputName == "InRelease" {
		plain, err = apt.ExtractClearSignedRelease(plain)
		if err != nil {
			return result, err
		}
	}
	if len(plain) == 0 || len(plain) > 32<<20 {
		return result, fmt.Errorf("Release 正文大小异常: %w", domain.ErrConflict)
	}

	work, err := os.MkdirTemp("", "airgap-sign-*")
	if err != nil {
		return result, err
	}
	defer os.RemoveAll(work)
	releaseFile := filepath.Join(work, "Release")
	inReleaseFile := filepath.Join(work, "InRelease")
	sigFile := filepath.Join(work, "Release.gpg")
	if err = os.WriteFile(releaseFile, plain, 0600); err != nil {
		return result, err
	}
	if _, err = s.gpg(ctx, "--yes", "--pinentry-mode", "loopback", "--local-user", s.KeyID, "--digest-algo", "SHA256", "--armor", "--clearsign", "--output", inReleaseFile, releaseFile); err != nil {
		return result, err
	}
	if _, err = s.gpg(ctx, "--yes", "--pinentry-mode", "loopback", "--local-user", s.KeyID, "--digest-algo", "SHA256", "--armor", "--detach-sign", "--output", sigFile, releaseFile); err != nil {
		return result, err
	}
	if _, err = s.gpg(ctx, "--verify", inReleaseFile); err != nil {
		return result, err
	}
	if _, err = s.gpg(ctx, "--verify", sigFile, releaseFile); err != nil {
		return result, err
	}

	// GPG 签名期间仍可能有其他后台工作，发布前再核对活动任务。
	recheck, err := s.Sources.GetState(ctx, sourceID)
	if err != nil {
		return result, err
	}
	if recheck.ActiveEpochID != "" || !recheck.LiveCursor.Equal(state.LiveCursor) || recheck.CatalogVersion != state.CatalogVersion {
		return result, fmt.Errorf("签名期间仓库状态发生变化，请重新清点: %w", domain.ErrConflict)
	}
	now := time.Now()
	files := []struct{ name, file string }{
		{"Release", releaseFile},
		{"Release.gpg", sigFile},
		{"InRelease", inReleaseFile},
	}
	for _, entry := range files {
		data, readErr := os.ReadFile(entry.file)
		if readErr != nil {
			return result, readErr
		}
		sum := sha256.Sum256(data)
		logicalPath := base + "/" + entry.name
		target, pathErr := safeTarget(source.RootPath, logicalPath)
		if pathErr != nil {
			return result, pathErr
		}
		digest := hex.EncodeToString(sum[:])
		if _, pathErr = writeVerified(source.RootPath, target, int64(len(data)), digest, bytes.NewReader(data), true); pathErr != nil {
			return result, pathErr
		}
		if pathErr = s.Catalog.Upsert(ctx, domain.CatalogEntry{
			SourceID: sourceID, LogicalPath: logicalPath, Size: int64(len(data)),
			SHA256: digest, UpdatedAt: now,
		}); pathErr != nil {
			return result, pathErr
		}
		result.Files = append(result.Files, logicalPath)
	}
	stats, err := s.Catalog.Stats(ctx, sourceID)
	if err != nil {
		return result, err
	}
	recheck.LiveBytes, recheck.LiveObjects = stats.Bytes, stats.Objects
	recheck.CatalogVersion++
	recheck.UpdatedAt = now
	if err = s.Sources.PutState(ctx, recheck); err != nil {
		return result, err
	}
	result.SourceID, result.Suite = sourceID, cfg.Suite
	return result, nil
}
