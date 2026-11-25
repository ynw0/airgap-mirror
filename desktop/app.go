package main

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"
	mirrorclient "github.com/ynw0/airgap-mirror/internal/client"
	"github.com/ynw0/airgap-mirror/internal/domain"
)

type App struct {
	ctx context.Context

	mu         sync.RWMutex
	workspace  *mirrorclient.Workspace
	agent      *mirrorclient.AgentClient
	startupErr error

	opsMu           sync.Mutex
	downloadCancels map[string]context.CancelFunc
	transferCancels map[string]context.CancelFunc
	reportCancels   map[string]context.CancelFunc
}

func NewApp() *App {
	return &App{
		downloadCancels: make(map[string]context.CancelFunc),
		transferCancels: make(map[string]context.CancelFunc),
		reportCancels:   make(map[string]context.CancelFunc),
	}
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	root, err := os.UserCacheDir()
	if err == nil {
		root = filepath.Join(root, "AirgapMirror")
		var workspace *mirrorclient.Workspace
		workspace, err = mirrorclient.OpenWorkspace(ctx, root, http.DefaultClient)
		if err == nil {
			a.mu.Lock()
			a.workspace = workspace
			a.mu.Unlock()
		}
	}
	if err != nil {
		a.mu.Lock()
		a.startupErr = err
		a.mu.Unlock()
	}
}

func (a *App) shutdown(context.Context) {
	a.opsMu.Lock()
	for _, cancel := range a.downloadCancels {
		cancel()
	}
	for _, cancel := range a.transferCancels {
		cancel()
	}
	for _, cancel := range a.reportCancels {
		cancel()
	}
	a.reportCancels = make(map[string]context.CancelFunc)
	a.downloadCancels = make(map[string]context.CancelFunc)
	a.transferCancels = make(map[string]context.CancelFunc)
	a.opsMu.Unlock()

	a.mu.Lock()
	workspace := a.workspace
	a.workspace = nil
	a.agent = nil
	a.mu.Unlock()
	if workspace != nil {
		_ = workspace.Close()
	}
}

func (a *App) workspaceRef() (*mirrorclient.Workspace, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.startupErr != nil {
		return nil, fmt.Errorf("desktop workspace startup failed: %w", a.startupErr)
	}
	if a.workspace == nil {
		return nil, fmt.Errorf("desktop workspace is unavailable: %w", domain.ErrInvalid)
	}
	return a.workspace, nil
}

func (a *App) agentRef() (*mirrorclient.AgentClient, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.agent == nil {
		return nil, fmt.Errorf("Agent is not connected: %w", domain.ErrInvalid)
	}
	return a.agent, nil
}

func (a *App) Bootstrap() (Bootstrap, error) {
	workspace, err := a.workspaceRef()
	if err != nil {
		return Bootstrap{}, err
	}
	capsules, err := workspace.ListCapsules(a.ctx)
	if err != nil {
		return Bootstrap{}, err
	}
	return Bootstrap{WorkspaceRoot: workspace.Root, Capsules: capsules}, nil
}

func loadAgentOverview(ctx context.Context, agent *mirrorclient.AgentClient) (AgentOverview, error) {
	if err := agent.Health(ctx); err != nil {
		return AgentOverview{}, err
	}
	sources, err := agent.ListSources(ctx)
	if err != nil {
		return AgentOverview{}, err
	}
	views := make([]AgentSourceView, 0, len(sources))
	for _, source := range sources {
		state, stateErr := agent.SourceState(ctx, source.ID)
		if stateErr != nil {
			return AgentOverview{}, fmt.Errorf("read source state %s: %w", source.Name, stateErr)
		}
		capacity, capacityErr := agent.Capacity(ctx, source.ID)
		view := AgentSourceView{Source: source, State: state, Capacity: capacity}
		if capacityErr != nil {
			view.CapacityError = capacityErr.Error()
		}
		views = append(views, view)
	}
	return AgentOverview{Connected: true, BaseURL: agent.BaseURL, Sources: views}, nil
}

type ConnectionCheck struct {
	Stage     string `json:"stage"`
	OK        bool   `json:"ok"`
	Message   string `json:"message"`
	ElapsedMS int64  `json:"elapsedMs"`
}

func connectionFailure(err error) string {
	var dns *net.DNSError
	var authority x509.UnknownAuthorityError
	var hostname x509.HostnameError
	var certificate x509.CertificateInvalidError
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "请求超时，请检查网络、服务器负载和反向代理。"
	case errors.As(err, &dns):
		return "无法解析服务器域名，请检查 DNS 或内网 hosts 配置。"
	case errors.As(err, &authority), errors.As(err, &hostname), errors.As(err, &certificate):
		return "HTTPS 证书验证失败，请检查证书、域名和信任链。不要关闭证书验证。"
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return "网络请求超时，请检查服务器和代理。"
	}
	var op *net.OpError
	if errors.As(err, &op) {
		return "无法建立网络连接，请检查监听地址、端口和防火墙。"
	}
	if errors.Is(err, domain.ErrNotFound) {
		return "接口不存在，请检查 Agent 地址及反向代理子路径。"
	}
	if errors.Is(err, domain.ErrInvalid) {
		return "服务器拒绝请求，请检查连接令牌、地址及访问权限。"
	}
	if strings.Contains(err.Error(), "decode agent response") {
		return "返回内容不是预期的 Agent JSON 响应，请检查反向代理路径。"
	}
	return "服务器未返回预期响应，请检查 Agent 及反向代理日志。"
}

// DiagnoseAgent uses read-only requests with a shared deadline and does not replace the active connection.
// The report deliberately omits raw response bodies, URLs and credentials.
func (a *App) DiagnoseAgent(baseURL, token string) []ConnectionCheck {
	start := time.Now()
	checks := make([]ConnectionCheck, 0, 3)
	add := func(stage string, ok bool, message string) {
		checks = append(checks, ConnectionCheck{Stage: stage, OK: ok, Message: message, ElapsedMS: time.Since(start).Milliseconds()})
	}
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		add("服务器地址", false, "请输入完整 HTTP 或 HTTPS 地址，可包含代理子路径，但不要包含用户名、密码、查询参数或片段。")
		return checks
	}
	agent, err := mirrorclient.NewAgent(baseURL, token, http.DefaultClient)
	if err != nil {
		add("服务器地址", false, "服务器地址格式不正确。")
		return checks
	}
	add("服务器地址", true, "地址格式正确。")
	ctx, cancel := context.WithTimeout(a.ctx, 15*time.Second)
	defer cancel()
	if err = agent.Health(ctx); err != nil {
		add("Agent 健康接口", false, connectionFailure(err))
		return checks
	}
	add("Agent 健康接口", true, "Agent 返回正常健康状态。")
	sources, err := agent.ListSources(ctx)
	if err != nil {
		add("软件源访问权限", false, connectionFailure(err))
		return checks
	}
	add("软件源访问权限", true, fmt.Sprintf("读取到 %d 个软件源。", len(sources)))
	return checks
}

func (a *App) ConnectAgent(baseURL, token string) (AgentOverview, error) {
	agent, err := mirrorclient.NewAgent(baseURL, token, http.DefaultClient)
	if err != nil {
		return AgentOverview{}, err
	}
	overview, err := loadAgentOverview(a.ctx, agent)
	if err != nil {
		return AgentOverview{}, err
	}
	a.mu.Lock()
	a.agent = agent
	a.mu.Unlock()
	return overview, nil
}

func (a *App) RefreshAgent() (AgentOverview, error) {
	agent, err := a.agentRef()
	if err != nil {
		return AgentOverview{}, err
	}
	return loadAgentOverview(a.ctx, agent)
}

func (a *App) ListAgentEpochs(sourceID string) ([]domain.Epoch, error) {
	agent, err := a.agentRef()
	if err != nil {
		return nil, err
	}
	return agent.ListEpochs(a.ctx, sourceID)
}

func (a *App) CancelEpoch(epochID string) (AgentOverview, error) {
	agent, err := a.agentRef()
	if err != nil {
		return AgentOverview{}, err
	}
	if _, err = agent.CancelEpoch(a.ctx, epochID); err != nil {
		return AgentOverview{}, err
	}
	return loadAgentOverview(a.ctx, agent)
}

func (a *App) DisconnectAgent() {
	a.mu.Lock()
	a.agent = nil
	a.mu.Unlock()
}

// SaveDiagnosticReport saves a bounded, explicitly selected UI report, not repository data.
func (a *App) SaveDiagnosticReport(report string) (string, error) {
	if len(report) == 0 || len(report) > 16*1024*1024 || !json.Valid([]byte(report)) {
		return "", fmt.Errorf("invalid diagnostic report or report exceeds 16 MiB: %w", domain.ErrInvalid)
	}
	destination, err := runtime.SaveFileDialog(a.ctx, runtime.SaveDialogOptions{
		Title:           "保存诊断报告",
		DefaultFilename: "airgap-diagnostics.json",
		Filters:         []runtime.FileFilter{{DisplayName: "JSON (*.json)", Pattern: "*.json"}},
	})
	if err != nil || destination == "" {
		return "", err
	}
	if filepath.Ext(destination) == "" {
		destination += ".json"
	}
	// Do not overwrite existing files, including symlinks.
	f, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return "", err
	}
	if _, err = f.WriteString(report); err != nil {
		_ = f.Close()
		_ = os.Remove(destination)
		return "", err
	}
	if err = f.Close(); err != nil {
		return "", err
	}
	return destination, nil
}

func (a *App) PickStateDestination() (string, error) {
	return runtime.SaveFileDialog(a.ctx, runtime.SaveDialogOptions{
		Title:           "保存 State Capsule",
		DefaultFilename: "mirror-state.mstate",
		Filters:         []runtime.FileFilter{{DisplayName: "Mirror State Capsule (*.mstate)", Pattern: "*.mstate"}},
	})
}

func (a *App) ExportStateCapsule(sourceIDs []string, destination string) (StateExportResult, error) {
	if destination == "" {
		return StateExportResult{}, fmt.Errorf("State Capsule destination is required: %w", domain.ErrInvalid)
	}
	agent, err := a.agentRef()
	if err != nil {
		return StateExportResult{}, err
	}
	exported, err := agent.ExportState(a.ctx, sourceIDs)
	if err != nil {
		return StateExportResult{}, err
	}
	if err = agent.DownloadState(a.ctx, exported.Name, destination); err != nil {
		return StateExportResult{}, err
	}
	return StateExportResult{Path: destination, Export: exported}, nil
}

func (a *App) PickCapsuleFile() (string, error) {
	return runtime.OpenFileDialog(a.ctx, runtime.OpenDialogOptions{
		Title:   "选择 State Capsule",
		Filters: []runtime.FileFilter{{DisplayName: "Mirror State Capsule (*.mstate)", Pattern: "*.mstate"}},
	})
}

func (a *App) PickDownloadDirectory() (string, error) {
	return runtime.OpenDirectoryDialog(a.ctx, runtime.OpenDialogOptions{
		Title:                "选择 Batch 下载目录或移动硬盘",
		CanCreateDirectories: true,
	})
}

func (a *App) PickBatchDirectory() (string, error) {
	return runtime.OpenDirectoryDialog(a.ctx, runtime.OpenDialogOptions{
		Title: "选择待导入的 Batch 目录",
	})
}

func (a *App) ImportCapsule(path string) (mirrorclient.ImportedCapsule, error) {
	workspace, err := a.workspaceRef()
	if err != nil {
		return mirrorclient.ImportedCapsule{}, err
	}
	return workspace.ImportCapsule(a.ctx, path)
}

func (a *App) ListCapsules() ([]domain.StateCapsule, error) {
	workspace, err := a.workspaceRef()
	if err != nil {
		return nil, err
	}
	return workspace.ListCapsules(a.ctx)
}

func (a *App) ProbeSource(capsuleID, sourceID string) (domain.UpstreamState, error) {
	workspace, err := a.workspaceRef()
	if err != nil {
		return domain.UpstreamState{}, err
	}
	return workspace.ProbeSource(a.ctx, capsuleID, sourceID)
}

func (a *App) AnalyzeSource(capsuleID, sourceID string, opts mirrorclient.AnalyzeOptions) (mirrorclient.SyncPlan, error) {
	workspace, err := a.workspaceRef()
	if err != nil {
		return mirrorclient.SyncPlan{}, err
	}
	return workspace.AnalyzeSource(a.ctx, capsuleID, sourceID, opts)
}

func (a *App) ListEpochs(capsuleID, sourceID string) ([]domain.Epoch, error) {
	workspace, err := a.workspaceRef()
	if err != nil {
		return nil, err
	}
	return workspace.ListEpochs(a.ctx, capsuleID, sourceID)
}

func (a *App) ResumeEpoch(epochID string) (EpochView, error) {
	workspace, err := a.workspaceRef()
	if err != nil {
		return EpochView{}, err
	}
	epoch, batches, err := workspace.ResumeEpoch(a.ctx, epochID)
	if err != nil {
		return EpochView{}, err
	}
	return EpochView{Epoch: epoch, Batches: batches}, nil
}

func (a *App) beginDownload(batchID string) (context.Context, func(), error) {
	a.opsMu.Lock()
	defer a.opsMu.Unlock()
	if _, exists := a.downloadCancels[batchID]; exists {
		return nil, nil, fmt.Errorf("batch %s download is already running: %w", batchID, domain.ErrConflict)
	}
	ctx, cancel := context.WithCancel(a.ctx)
	a.downloadCancels[batchID] = cancel
	cleanup := func() {
		a.opsMu.Lock()
		delete(a.downloadCancels, batchID)
		a.opsMu.Unlock()
		cancel()
	}
	return ctx, cleanup, nil
}

func (a *App) DownloadBatch(batchID, destination string, concurrency int) (mirrorclient.BatchBundle, error) {
	workspace, err := a.workspaceRef()
	if err != nil {
		return mirrorclient.BatchBundle{}, err
	}
	ctx, cleanup, err := a.beginDownload(batchID)
	if err != nil {
		return mirrorclient.BatchBundle{}, err
	}
	defer cleanup()
	runtime.EventsEmit(a.ctx, "download-state", map[string]any{"batchId": batchID, "state": "running"})
	bundle, err := workspace.DownloadBatch(ctx, batchID, mirrorclient.DownloadOptions{DestinationRoot: destination, Concurrency: concurrency})
	if err != nil {
		runtime.EventsEmit(a.ctx, "download-state", map[string]any{"batchId": batchID, "state": "stopped", "error": err.Error()})
		return mirrorclient.BatchBundle{}, err
	}
	runtime.EventsEmit(a.ctx, "download-state", map[string]any{"batchId": batchID, "state": "ready", "path": bundle.Path})
	return bundle, nil
}

func (a *App) RedownloadBatch(batchID, destination string, concurrency int) (mirrorclient.BatchBundle, error) {
	workspace, err := a.workspaceRef()
	if err != nil {
		return mirrorclient.BatchBundle{}, err
	}
	ctx, cleanup, err := a.beginDownload(batchID)
	if err != nil {
		return mirrorclient.BatchBundle{}, err
	}
	defer cleanup()
	if err = workspace.RedownloadReadyBatch(ctx, batchID, destination); err != nil {
		return mirrorclient.BatchBundle{}, err
	}
	runtime.EventsEmit(a.ctx, "download-state", map[string]any{"batchId": batchID, "state": "running"})
	bundle, err := workspace.DownloadBatch(ctx, batchID, mirrorclient.DownloadOptions{DestinationRoot: destination, Concurrency: concurrency})
	if err != nil {
		runtime.EventsEmit(a.ctx, "download-state", map[string]any{"batchId": batchID, "state": "stopped", "error": err.Error()})
		return mirrorclient.BatchBundle{}, err
	}
	runtime.EventsEmit(a.ctx, "download-state", map[string]any{"batchId": batchID, "state": "ready", "path": bundle.Path})
	return bundle, nil
}

func (a *App) PauseDownload(batchID string) error {
	a.opsMu.Lock()
	cancel, ok := a.downloadCancels[batchID]
	a.opsMu.Unlock()
	if !ok {
		return fmt.Errorf("batch %s has no running download: %w", batchID, domain.ErrNotFound)
	}
	cancel()
	return nil
}

func (a *App) BatchProgress(batchID string) (mirrorclient.BatchDownloadProgress, error) {
	workspace, err := a.workspaceRef()
	if err != nil {
		return mirrorclient.BatchDownloadProgress{}, err
	}
	return workspace.BatchProgress(a.ctx, batchID)
}

func (a *App) ValidateBatchDirectory(path string) (mirrorclient.ValidatedBundle, error) {
	return mirrorclient.ValidateBatchBundle(a.ctx, path)
}

func (a *App) beginTransfer(batchID string) (context.Context, func(), error) {
	a.opsMu.Lock()
	defer a.opsMu.Unlock()
	if _, exists := a.transferCancels[batchID]; exists {
		return nil, nil, fmt.Errorf("batch %s transfer is already running: %w", batchID, domain.ErrConflict)
	}
	ctx, cancel := context.WithCancel(a.ctx)
	a.transferCancels[batchID] = cancel
	cleanup := func() {
		a.opsMu.Lock()
		delete(a.transferCancels, batchID)
		a.opsMu.Unlock()
		cancel()
	}
	return ctx, cleanup, nil
}

func (a *App) TransferBatchDirectory(path string, opts mirrorclient.BundleTransferOptions) (DesktopTransferResult, error) {
	validated, err := mirrorclient.ValidateBatchBundle(a.ctx, path)
	if err != nil {
		return DesktopTransferResult{}, err
	}
	batchID := validated.Descriptor.BatchID
	ctx, cleanup, err := a.beginTransfer(batchID)
	if err != nil {
		return DesktopTransferResult{}, err
	}
	defer cleanup()
	agent, err := a.agentRef()
	if err != nil {
		return DesktopTransferResult{}, err
	}
	workspace, err := a.workspaceRef()
	if err != nil {
		return DesktopTransferResult{}, err
	}
	progress := func(p mirrorclient.TransferProgress) error {
		runtime.EventsEmit(a.ctx, "transfer-progress", p)
		return nil
	}
	if _, lookupErr := workspace.Batch(ctx, batchID); lookupErr == nil {
		local, transferErr := workspace.TransferLocalBundle(ctx, agent, path, opts, progress)
		if transferErr != nil {
			return DesktopTransferResult{}, transferErr
		}
		return DesktopTransferResult{WorkspaceTracked: true, Local: &local}, nil
	} else if !errors.Is(lookupErr, domain.ErrNotFound) {
		return DesktopTransferResult{}, lookupErr
	}
	standalone, err := mirrorclient.TransferBatchBundle(ctx, agent, path, opts, progress)
	if err != nil {
		return DesktopTransferResult{}, err
	}
	return DesktopTransferResult{WorkspaceTracked: false, Standalone: &standalone}, nil
}

func (a *App) PauseTransfer(batchID string) error {
	a.opsMu.Lock()
	cancel, ok := a.transferCancels[batchID]
	a.opsMu.Unlock()
	if !ok {
		return fmt.Errorf("batch %s has no running transfer: %w", batchID, domain.ErrNotFound)
	}
	cancel()
	return nil
}
