package service

import (
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"github.com/ynw0/airgap-mirror/internal/domain"
	"github.com/ynw0/airgap-mirror/internal/pack"
	"github.com/ynw0/airgap-mirror/internal/ports"
	"hash"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

type Downloader struct {
	Plans         ports.PlanStore
	Exec          ports.TransferExecutionStore
	Progress      ports.DownloadProgressStore
	Fetcher       ports.ArtifactFetcher
	Concurrency   int
	ProgressEvery int64
}

func (d Downloader) DownloadBatch(ctx context.Context, bid, out string) error {
	ps, e := d.Exec.ListPacks(ctx, bid)
	if e != nil {
		return e
	}
	n := d.Concurrency
	if n <= 0 {
		n = 4
	}
	jobs := make(chan domain.Pack)
	errs := make(chan error, 1)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for p := range jobs {
				if x := d.downloadPack(ctx, p, out); x != nil {
					select {
					case errs <- x:
						cancel()
					default:
					}
					return
				}
			}
		}()
	}
loop:
	for _, p := range ps {
		select {
		case jobs <- p:
		case <-ctx.Done():
			break loop
		}
	}
	close(jobs)
	wg.Wait()
	select {
	case x := <-errs:
		return x
	default:
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return nil
	}
}
func (d Downloader) downloadPack(ctx context.Context, p domain.Pack, out string) error {
	dir := filepath.Join(out, "packs")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	pth := filepath.Join(dir, fmt.Sprintf("%06d.agp", p.Sequence))
	h := domain.PackHeader{PackID: p.ID, EpochID: p.EpochID, BatchID: p.BatchID}
	var w *pack.Writer
	var err error
	existed := false
	if _, statErr := os.Stat(pth); statErr == nil {
		existed = true
		w, err = pack.Resume(pth, h)
	} else if os.IsNotExist(statErr) {
		w, err = pack.Create(pth, h)
	} else {
		return statErr
	}
	if err != nil {
		return err
	}

	var existing *pack.Reader
	if existed {
		existing, err = pack.Open(pth)
		if err != nil {
			_ = w.Close()
			return err
		}
		defer existing.Close()
	}
	existingDone := !existed

	err = d.Exec.WalkPackArtifacts(ctx, p.ID, func(a domain.Artifact) error {
		st, stateErr := d.Progress.EntryState(ctx, a.ID)
		if stateErr != nil && stateErr != domain.ErrNotFound {
			return stateErr
		}

		if !existingDone {
			rec, body, readErr := existing.Next(ctx)
			if readErr == io.EOF {
				existingDone = true
			} else if readErr != nil {
				return readErr
			} else {
				if rec.EntryID != a.ID || rec.LogicalPath != a.LogicalPath || rec.ContentLength != a.Size || (a.SHA256 != "" && rec.SHA256 != strings.ToLower(a.SHA256)) {
					return fmt.Errorf("pack/database resume prefix mismatch for artifact %s: %w", a.ID, domain.ErrConflict)
				}
				hash := sha256.New()
				n, copyErr := io.Copy(hash, body)
				if copyErr != nil {
					return copyErr
				}
				if n != rec.ContentLength || hex.EncodeToString(hash.Sum(nil)) != rec.SHA256 {
					return fmt.Errorf("existing pack record integrity mismatch for artifact %s: %w", a.ID, domain.ErrConflict)
				}
				if st.Status != domain.DownloadVerified {
					if err := d.Plans.FinalizeArtifact(ctx, a.ID, rec.ContentLength, rec.SHA256); err != nil {
						return err
					}
					if err := d.Plans.SetArtifactPackLocation(ctx, a.ID, domain.PackLocation{PackID: p.ID, Offset: rec.Offset, RecordLength: rec.RecordLength}); err != nil {
						return err
					}
					if err := d.Progress.MarkEntryState(ctx, domain.DownloadEntryProgress{EntryID: a.ID, Status: domain.DownloadVerified, Downloaded: rec.ContentLength}); err != nil {
						return err
					}
				}
				return nil
			}
		}

		if st.Status == domain.DownloadVerified {
			return fmt.Errorf("database marks artifact %s VERIFIED but pack has no matching record: %w", a.ID, domain.ErrConflict)
		}
		if a.Operation == domain.ArtifactDelete {
			if !a.Metadata || a.Size != 0 || a.SHA256 == "" || a.LocalSourcePath != "" || a.UpstreamURL != "" {
				return fmt.Errorf("invalid metadata tombstone %s: %w", a.ID, domain.ErrInvalid)
			}
			loc, appendErr := w.Append(ctx, domain.PackEntry{Artifact: a}, strings.NewReader(""))
			if appendErr != nil {
				return appendErr
			}
			if err := d.Plans.SetArtifactPackLocation(ctx, a.ID, loc); err != nil {
				return err
			}
			return d.Progress.MarkEntryState(ctx, domain.DownloadEntryProgress{EntryID: a.ID, Status: domain.DownloadVerified, Downloaded: 0})
		}

		tmp := pth + "." + a.ID + ".part"
		sourcePath := tmp
		removeSource := true
		var sha string
		var size int64
		var materializeErr error
		switch {
		case a.LocalSourcePath != "" && a.UpstreamURL != "":
			return fmt.Errorf("artifact %s has both local and upstream sources: %w", a.ID, domain.ErrInvalid)
		case a.LocalSourcePath != "":
			sourcePath = a.LocalSourcePath
			removeSource = false
			sha, size, materializeErr = verifyFile(sourcePath, a.UpstreamIntegrity)
			if materializeErr == nil && (size != a.Size || a.SHA256 == "" || sha != strings.ToLower(a.SHA256)) {
				materializeErr = fmt.Errorf("generated artifact %s integrity mismatch: %w", a.ID, domain.ErrConflict)
			}
		case a.UpstreamURL != "":
			sha, size, materializeErr = d.downloadArtifact(ctx, a, tmp, st)
		default:
			return fmt.Errorf("artifact %s has no content source: %w", a.ID, domain.ErrInvalid)
		}
		if materializeErr != nil {
			return materializeErr
		}
		if materializeErr = d.Plans.FinalizeArtifact(ctx, a.ID, size, sha); materializeErr != nil {
			return materializeErr
		}
		a.SHA256 = sha
		f, openErr := os.Open(sourcePath)
		if openErr != nil {
			return openErr
		}
		loc, appendErr := w.Append(ctx, domain.PackEntry{Artifact: a}, f)
		closeErr := f.Close()
		if appendErr != nil {
			return appendErr
		}
		if closeErr != nil {
			return closeErr
		}
		if err := d.Plans.SetArtifactPackLocation(ctx, a.ID, loc); err != nil {
			return err
		}
		if removeSource {
			if err := os.Remove(tmp); err != nil {
				return err
			}
		}
		return d.Progress.MarkEntryState(ctx, domain.DownloadEntryProgress{EntryID: a.ID, Status: domain.DownloadVerified, Downloaded: size})
	})
	if err != nil {
		_ = w.Close()
		return err
	}
	if existing != nil && !existingDone {
		if rec, body, readErr := existing.Next(ctx); readErr == nil {
			_, _ = io.Copy(io.Discard, body)
			return fmt.Errorf("pack contains undeclared trailing entry %s: %w", rec.EntryID, domain.ErrConflict)
		} else if readErr != io.EOF {
			return readErr
		}
	}
	if err = w.Close(); err != nil {
		return err
	}
	sha, size, err := pack.FileSHA256(pth)
	if err != nil {
		return err
	}
	p.FilePath = pth
	p.Size = size
	p.SHA256 = sha
	p.Status = domain.PackReady
	return d.Exec.UpdatePack(ctx, p)
}

func (d Downloader) downloadArtifact(ctx context.Context, a domain.Artifact, tmp string, st domain.DownloadEntryProgress) (string, int64, error) {
	var off int64
	if f, e := os.Stat(tmp); e == nil {
		off = f.Size()
	}
	if st.Downloaded != 0 && st.Downloaded != off {
		return "", 0, fmt.Errorf("download offset mismatch")
	}
	f, e := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if e != nil {
		return "", 0, e
	}
	defer f.Close()
	r, e := d.Fetcher.Open(ctx, ports.FetchRequest{URL: a.UpstreamURL, Offset: off, ETag: st.ETag, LastModified: st.LastModified})
	if e != nil {
		return "", 0, e
	}
	defer r.Body.Close()
	if r.StatusCode < 200 || r.StatusCode >= 300 {
		return "", 0, fmt.Errorf("http %d", r.StatusCode)
	}
	buf := make([]byte, 4<<20)
	written := off
	step := d.ProgressEvery
	if step <= 0 {
		step = 64 << 20
	}
	next := written + step
	for {
		n, x := r.Body.Read(buf)
		if n > 0 {
			if _, e = f.Write(buf[:n]); e != nil {
				return "", written, e
			}
			written += int64(n)
			if written >= next {
				if e = d.Progress.MarkEntryState(ctx, domain.DownloadEntryProgress{EntryID: a.ID, Status: domain.DownloadDownloading, Downloaded: written, ETag: r.ETag, LastModified: r.LastModified}); e != nil {
					return "", written, e
				}
				next = written + step
			}
		}
		if x == io.EOF {
			break
		}
		if x != nil {
			return "", written, x
		}
	}
	if a.Size > 0 && written != a.Size {
		return "", written, fmt.Errorf("size mismatch")
	}
	sha, _, e := verifyFile(tmp, a.UpstreamIntegrity)
	if e != nil {
		return "", written, e
	}
	if a.SHA256 != "" && sha != strings.ToLower(a.SHA256) {
		return "", written, fmt.Errorf("sha256 mismatch")
	}
	return sha, written, nil
}
func verifyFile(p, integrity string) (string, int64, error) {
	f, e := os.Open(p)
	if e != nil {
		return "", 0, e
	}
	defer f.Close()
	h := sha256.New()
	var x hash.Hash
	var want []byte
	if integrity != "" {
		alg, val, ok := strings.Cut(integrity, "-")
		if !ok {
			return "", 0, fmt.Errorf("invalid integrity")
		}
		want, e = base64.StdEncoding.DecodeString(val)
		if e != nil {
			return "", 0, e
		}
		switch strings.ToLower(alg) {
		case "sha512":
			x = sha512.New()
		case "sha384":
			x = sha512.New384()
		case "sha256":
			x = sha256.New()
		case "sha1":
			x = sha1.New()
		default:
			return "", 0, fmt.Errorf("unsupported integrity %s", alg)
		}
	}
	ws := []io.Writer{h}
	if x != nil {
		ws = append(ws, x)
	}
	n, e := io.Copy(io.MultiWriter(ws...), f)
	if e != nil {
		return "", n, e
	}
	if x != nil && !equalBytes(x.Sum(nil), want) {
		return "", n, fmt.Errorf("upstream integrity mismatch")
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}
func equalBytes(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	var x byte
	for i := range a {
		x |= a[i] ^ b[i]
	}
	return x == 0
}
