package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ynw0/airgap-mirror/internal/domain"
	"github.com/ynw0/airgap-mirror/internal/pack"
	"github.com/ynw0/airgap-mirror/internal/ports"
)

type GeneratedStore struct {
	Root string
}

func (s GeneratedStore) Put(ctx context.Context, epochID, logicalPath string, content []byte) (string, string, int64, error) {
	if err := ctx.Err(); err != nil {
		return "", "", 0, err
	}
	if s.Root == "" || epochID == "" {
		return "", "", 0, fmt.Errorf("generated store root and epoch are required: %w", domain.ErrInvalid)
	}
	if err := pack.ValidateLogicalPath(logicalPath); err != nil {
		return "", "", 0, err
	}
	contentHash := sha256.Sum256(content)
	sha := hex.EncodeToString(contentHash[:])
	nameHash := sha256.Sum256([]byte(logicalPath))
	dir := filepath.Join(s.Root, epochID)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", "", 0, err
	}
	final := filepath.Join(dir, hex.EncodeToString(nameHash[:])+".generated")
	if oldHash, oldSize, err := hashFile(final); err == nil {
		if oldHash != sha || oldSize != int64(len(content)) {
			return "", "", 0, fmt.Errorf("generated artifact changed within epoch for %s: %w", logicalPath, domain.ErrConflict)
		}
		return final, sha, oldSize, nil
	} else if !os.IsNotExist(err) {
		return "", "", 0, err
	}
	tmp, err := os.CreateTemp(dir, ".generated-*")
	if err != nil {
		return "", "", 0, err
	}
	tmpName := tmp.Name()
	ok := false
	defer func() {
		_ = tmp.Close()
		if !ok {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err = tmp.Write(content); err != nil {
		return "", "", 0, err
	}
	if err = tmp.Sync(); err != nil {
		return "", "", 0, err
	}
	if err = tmp.Close(); err != nil {
		return "", "", 0, err
	}
	if err = os.Rename(tmpName, final); err != nil {
		return "", "", 0, err
	}
	ok = true
	return final, sha, int64(len(content)), nil
}

var _ ports.GeneratedArtifactStore = GeneratedStore{}
