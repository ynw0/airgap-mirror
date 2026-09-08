package client

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/ynw0/airgap-mirror/internal/domain"
)

const DefaultUploadChunk int64 = 64 << 20

type TransferProgress struct {
	Kind        string `json:"kind"`
	SessionID   string `json:"sessionId"`
	PackID      string `json:"packId,omitempty"`
	Path        string `json:"path"`
	Transferred int64  `json:"transferred"`
	Total       int64  `json:"total"`
}

type ProgressFunc func(TransferProgress) error

func (c *AgentClient) uploadOffset(ctx context.Context, requestPath string) (int64, int64, error) {
	req, err := c.newRequest(ctx, http.MethodHead, requestPath, nil)
	if err != nil {
		return 0, 0, err
	}
	resp, err := c.do(req)
	if err != nil {
		return 0, 0, err
	}
	defer resp.Body.Close()
	return parseUploadHeaders(resp)
}

func (c *AgentClient) uploadFile(ctx context.Context, requestPath, localPath, kind, sessionID, packID string, chunkSize int64, expectedSHA256 string, progress ProgressFunc) error {
	if chunkSize <= 0 {
		chunkSize = DefaultUploadChunk
	}
	if chunkSize > 128<<20 {
		return fmt.Errorf("upload chunk exceeds server maximum: %w", domain.ErrInvalid)
	}
	if expectedSHA256 != "" {
		expectedSHA256 = strings.ToLower(strings.TrimSpace(expectedSHA256))
		if len(expectedSHA256) != sha256.Size*2 {
			return fmt.Errorf("invalid expected sha256: %w", domain.ErrInvalid)
		}
		if _, err := hex.DecodeString(expectedSHA256); err != nil {
			return fmt.Errorf("invalid expected sha256: %w", domain.ErrInvalid)
		}
	}
	f, err := os.Open(localPath)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("upload source is not a regular file: %w", domain.ErrInvalid)
	}
	offset, length, err := c.uploadOffset(ctx, requestPath)
	if err != nil {
		return err
	}
	if info.Size() != length {
		return fmt.Errorf("local file size %d != server declaration %d: %w", info.Size(), length, domain.ErrConflict)
	}

	var hasher = sha256.New()
	if expectedSHA256 == "" {
		hasher = nil
	} else if offset > 0 {
		if _, err = io.Copy(hasher, io.NewSectionReader(f, 0, offset)); err != nil {
			return fmt.Errorf("hash uploaded prefix: %w", err)
		}
	}

	if progress != nil {
		if err = progress(TransferProgress{Kind: kind, SessionID: sessionID, PackID: packID, Path: localPath, Transferred: offset, Total: length}); err != nil {
			return err
		}
	}
	for offset < length {
		if err = ctx.Err(); err != nil {
			return err
		}
		n := chunkSize
		if left := length - offset; left < n {
			n = left
		}
		section := io.NewSectionReader(f, offset, n)
		var body io.Reader = section
		if hasher != nil {
			body = io.TeeReader(section, hasher)
		}
		req, err := c.newRequest(ctx, http.MethodPatch, requestPath, body)
		if err != nil {
			return err
		}
		req.ContentLength = n
		req.Header.Set("Content-Type", "application/offset+octet-stream")
		req.Header.Set("Upload-Offset", strconv.FormatInt(offset, 10))
		resp, err := c.do(req)
		if err != nil {
			return err
		}
		nextRaw := resp.Header.Get("Upload-Offset")
		_ = resp.Body.Close()
		next, parseErr := strconv.ParseInt(nextRaw, 10, 64)
		if parseErr != nil || next != offset+n {
			return fmt.Errorf("server upload offset %q after %d bytes: %w", nextRaw, n, domain.ErrConflict)
		}
		offset = next
		if progress != nil {
			if err = progress(TransferProgress{Kind: kind, SessionID: sessionID, PackID: packID, Path: localPath, Transferred: offset, Total: length}); err != nil {
				return err
			}
		}
	}
	if hasher != nil {
		if got := hex.EncodeToString(hasher.Sum(nil)); got != expectedSHA256 {
			return fmt.Errorf("local upload source sha256 %s != expected %s: %w", got, expectedSHA256, domain.ErrConflict)
		}
	}
	return nil
}

func (c *AgentClient) UploadManifest(ctx context.Context, sessionID, manifestPath string, chunkSize int64, progress ProgressFunc) error {
	requestPath := "/api/v1/imports/" + url.PathEscape(sessionID) + "/manifest"
	return c.uploadFile(ctx, requestPath, manifestPath, "manifest", sessionID, "", chunkSize, "", progress)
}

func (c *AgentClient) UploadManifestVerified(ctx context.Context, sessionID, manifestPath, expectedSHA256 string, chunkSize int64, progress ProgressFunc) error {
	requestPath := "/api/v1/imports/" + url.PathEscape(sessionID) + "/manifest"
	return c.uploadFile(ctx, requestPath, manifestPath, "manifest", sessionID, "", chunkSize, expectedSHA256, progress)
}

func (c *AgentClient) CompleteManifest(ctx context.Context, sessionID string) error {
	return c.jsonRequest(ctx, http.MethodPost, "/api/v1/imports/"+url.PathEscape(sessionID)+"/manifest/complete", nil, nil)
}

func (c *AgentClient) UploadPack(ctx context.Context, sessionID, packID, packPath string, chunkSize int64, progress ProgressFunc) error {
	requestPath := "/api/v1/imports/" + url.PathEscape(sessionID) + "/packs/" + url.PathEscape(packID)
	return c.uploadFile(ctx, requestPath, packPath, "pack", sessionID, packID, chunkSize, "", progress)
}

func (c *AgentClient) UploadPackVerified(ctx context.Context, sessionID, packID, packPath, expectedSHA256 string, chunkSize int64, progress ProgressFunc) error {
	requestPath := "/api/v1/imports/" + url.PathEscape(sessionID) + "/packs/" + url.PathEscape(packID)
	return c.uploadFile(ctx, requestPath, packPath, "pack", sessionID, packID, chunkSize, expectedSHA256, progress)
}

func (c *AgentClient) CommitPack(ctx context.Context, sessionID, packID string) error {
	requestPath := "/api/v1/imports/" + url.PathEscape(sessionID) + "/packs/" + url.PathEscape(packID) + "/commit"
	return c.jsonRequest(ctx, http.MethodPost, requestPath, nil, nil)
}

func (c *AgentClient) CompleteBatch(ctx context.Context, sessionID string) error {
	return c.jsonRequest(ctx, http.MethodPost, "/api/v1/imports/"+url.PathEscape(sessionID)+"/complete", nil, nil)
}
