package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ynw0/airgap-mirror/internal/domain"
)

const maxControlResponse = 32 << 20

type AgentClient struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
}

type StateExport struct {
	Capsule  domain.StateCapsule `json:"capsule"`
	Name     string              `json:"name"`
	Download string              `json:"download"`
}

type ImportStatus struct {
	Session domain.ImportSession `json:"session"`
	Packs   []domain.ImportPack  `json:"packs"`
}

type apiError struct {
	Error string `json:"error"`
}

func NewAgent(baseURL, token string, httpClient *http.Client) (*AgentClient, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	u, err := url.ParseRequestURI(baseURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, fmt.Errorf("agent URL must be absolute HTTP(S): %w", domain.ErrInvalid)
	}
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &AgentClient{BaseURL: baseURL, Token: token, HTTP: httpClient}, nil
}

func (c *AgentClient) newRequest(ctx context.Context, method, requestPath string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+requestPath, body)
	if err != nil {
		return nil, err
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	return req, nil
}

func mapStatus(status int, message string) error {
	if message == "" {
		message = http.StatusText(status)
	}
	var kind error
	switch status {
	case http.StatusBadRequest:
		kind = domain.ErrInvalid
	case http.StatusNotFound:
		kind = domain.ErrNotFound
	case http.StatusConflict:
		kind = domain.ErrConflict
	case http.StatusUnauthorized, http.StatusForbidden:
		kind = domain.ErrInvalid
	default:
		kind = fmt.Errorf("agent http status %d", status)
	}
	return fmt.Errorf("agent: %s: %w", message, kind)
}

func (c *AgentClient) do(req *http.Request) (*http.Response, error) {
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return resp, nil
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var payload apiError
	message := strings.TrimSpace(string(body))
	if json.Unmarshal(body, &payload) == nil && payload.Error != "" {
		message = payload.Error
	}
	return nil, mapStatus(resp.StatusCode, message)
}

func (c *AgentClient) jsonRequest(ctx context.Context, method, requestPath string, input any, output any) error {
	var body io.Reader
	if input != nil {
		data, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}
	req, err := c.newRequest(ctx, method, requestPath, body)
	if err != nil {
		return err
	}
	if input != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if output == nil || resp.StatusCode == http.StatusNoContent {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	dec := json.NewDecoder(io.LimitReader(resp.Body, maxControlResponse))
	if err = dec.Decode(output); err != nil {
		return fmt.Errorf("decode agent response: %w", err)
	}
	return nil
}

func (c *AgentClient) Health(ctx context.Context) error {
	var payload struct {
		Status string `json:"status"`
	}
	if err := c.jsonRequest(ctx, http.MethodGet, "/api/v1/health", nil, &payload); err != nil {
		return err
	}
	if payload.Status != "ok" {
		return fmt.Errorf("unexpected agent health %q: %w", payload.Status, domain.ErrConflict)
	}
	return nil
}

func (c *AgentClient) ListSources(ctx context.Context) ([]domain.Source, error) {
	var out []domain.Source
	err := c.jsonRequest(ctx, http.MethodGet, "/api/v1/sources", nil, &out)
	return out, err
}

func (c *AgentClient) SourceState(ctx context.Context, sourceID string) (domain.SourceState, error) {
	var out domain.SourceState
	err := c.jsonRequest(ctx, http.MethodGet, "/api/v1/sources/"+url.PathEscape(sourceID)+"/state", nil, &out)
	return out, err
}

func (c *AgentClient) Capacity(ctx context.Context, sourceID string) (domain.Capacity, error) {
	var out domain.Capacity
	query := url.Values{"sourceId": []string{sourceID}}
	err := c.jsonRequest(ctx, http.MethodGet, "/api/v1/capacity?"+query.Encode(), nil, &out)
	return out, err
}

func (c *AgentClient) ExportState(ctx context.Context, sourceIDs []string) (StateExport, error) {
	var out StateExport
	err := c.jsonRequest(ctx, http.MethodPost, "/api/v1/state/export", map[string]any{"sourceIds": sourceIDs}, &out)
	return out, err
}

func (c *AgentClient) DownloadState(ctx context.Context, exportName, destination string) error {
	if exportName == "" || filepath.Base(exportName) != exportName {
		return fmt.Errorf("invalid State Capsule export name: %w", domain.ErrInvalid)
	}
	if destination == "" {
		return fmt.Errorf("State Capsule destination is required: %w", domain.ErrInvalid)
	}
	req, err := c.newRequest(ctx, http.MethodGet, "/api/v1/state/exports/"+url.PathEscape(exportName), nil)
	if err != nil {
		return err
	}
	resp, err := c.do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err = os.MkdirAll(filepath.Dir(destination), 0755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(destination), ".mstate-download-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	committed := false
	defer func() {
		_ = tmp.Close()
		if !committed {
			_ = os.Remove(tmpName)
		}
	}()
	n, err := io.Copy(tmp, resp.Body)
	if err != nil {
		return err
	}
	if resp.ContentLength >= 0 && n != resp.ContentLength {
		return fmt.Errorf("State Capsule download size %d != %d: %w", n, resp.ContentLength, domain.ErrConflict)
	}
	if err = tmp.Sync(); err != nil {
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	if _, err = os.Stat(destination); err == nil {
		return fmt.Errorf("State Capsule destination already exists: %w", domain.ErrConflict)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err = os.Rename(tmpName, destination); err != nil {
		return err
	}
	committed = true
	return nil
}

func (c *AgentClient) CreateImport(ctx context.Context, requestID string, descriptor domain.BatchDescriptor) (domain.ImportSession, error) {
	var out domain.ImportSession
	if requestID == "" {
		return out, fmt.Errorf("requestId is required: %w", domain.ErrInvalid)
	}
	err := c.jsonRequest(ctx, http.MethodPost, "/api/v1/imports", map[string]any{"requestId": requestID, "descriptor": descriptor}, &out)
	return out, err
}

func (c *AgentClient) ImportStatus(ctx context.Context, sessionID string) (ImportStatus, error) {
	var out ImportStatus
	err := c.jsonRequest(ctx, http.MethodGet, "/api/v1/imports/"+url.PathEscape(sessionID), nil, &out)
	return out, err
}

func parseUploadHeaders(resp *http.Response) (offset, length int64, err error) {
	offset, err = strconv.ParseInt(resp.Header.Get("Upload-Offset"), 10, 64)
	if err != nil || offset < 0 {
		return 0, 0, fmt.Errorf("invalid Upload-Offset: %w", domain.ErrConflict)
	}
	length, err = strconv.ParseInt(resp.Header.Get("Upload-Length"), 10, 64)
	if err != nil || length < 0 || offset > length {
		return 0, 0, fmt.Errorf("invalid Upload-Length: %w", domain.ErrConflict)
	}
	return offset, length, nil
}
