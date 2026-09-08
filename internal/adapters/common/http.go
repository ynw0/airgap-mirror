package common

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const UserAgent = "airgap-mirror/1"

func Client(c *http.Client) *http.Client {
	if c != nil {
		return c
	}
	return http.DefaultClient
}

func NewRequest(ctx context.Context, method, rawURL string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", UserAgent)
	return req, nil
}

func Do(ctx context.Context, c *http.Client, method, rawURL string, accept string) (*http.Response, error) {
	req, err := NewRequest(ctx, method, rawURL)
	if err != nil {
		return nil, err
	}
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	resp, err := Client(c).Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
		return nil, fmt.Errorf("%s %s: http %d: %s", method, rawURL, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return resp, nil
}

func ReadAllSHA256(r io.Reader, max int64) ([]byte, string, error) {
	h := sha256.New()
	var reader io.Reader = r
	if max > 0 {
		reader = io.LimitReader(r, max+1)
	}
	body, err := io.ReadAll(io.TeeReader(reader, h))
	if err != nil {
		return nil, "", err
	}
	if max > 0 && int64(len(body)) > max {
		return nil, "", fmt.Errorf("response exceeds %d bytes", max)
	}
	return body, hex.EncodeToString(h.Sum(nil)), nil
}
