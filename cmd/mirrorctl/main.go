package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func main() {
	var base, token string
	flag.StringVar(&base, "url", "http://127.0.0.1:8787", "Mirror Agent URL")
	flag.StringVar(&token, "token", os.Getenv("AIRGAP_MIRROR_TOKEN"), "Bearer token")
	flag.Parse()
	method, path, body, dest, ok := parseCommand(flag.Args())
	if !ok {
		fmt.Fprintln(os.Stderr, "usage: mirrorctl [flags] health|sources|sources-get <id>|sources-state <id>|capacity:<id>|sources-create <file.json>|sources-update <id> <file.json>|epochs [source-id]|export [source-id...]|export-get <name> <file>|inventory <id>|gc <id> [execute]|gc-candidates <job-id>|maintenance [source-id]|maintenance-get <job-id>|maintenance-cancel <job-id>")
		os.Exit(2)
	}
	code := request(method, strings.TrimRight(base, "/")+path, token, body, dest)
	os.Exit(code)
}

func parseCommand(args []string) (method, path string, body []byte, dest string, ok bool) {
	if len(args) == 0 {
		return "", "", nil, "", false
	}
	cmd := args[0]
	switch {
	case cmd == "health" && len(args) == 1:
		return http.MethodGet, "/api/v1/health", nil, "", true
	case cmd == "sources" && len(args) == 1:
		return http.MethodGet, "/api/v1/sources", nil, "", true
	case cmd == "sources-get" && len(args) == 2:
		return http.MethodGet, "/api/v1/sources/" + url.PathEscape(args[1]), nil, "", true
	case cmd == "sources-state" && len(args) == 2:
		return http.MethodGet, "/api/v1/sources/" + url.PathEscape(args[1]) + "/state", nil, "", true
	case strings.HasPrefix(cmd, "capacity:") && len(args) == 1:
		return http.MethodGet, "/api/v1/capacity?sourceId=" + url.QueryEscape(strings.TrimPrefix(cmd, "capacity:")), nil, "", true
	case cmd == "sources-create" && len(args) == 2:
		return http.MethodPost, "/api/v1/sources", mustRead(args[1]), "", true
	case cmd == "sources-update" && len(args) == 3:
		return http.MethodPut, "/api/v1/sources/" + url.PathEscape(args[1]), mustRead(args[2]), "", true
	case cmd == "epochs" && len(args) == 1:
		return http.MethodGet, "/api/v1/epochs", nil, "", true
	case cmd == "epochs" && len(args) == 2:
		return http.MethodGet, "/api/v1/epochs?sourceId=" + url.QueryEscape(args[1]), nil, "", true
	case cmd == "export":
		payload := []byte(`{}`)
		if len(args) > 1 {
			encoded, err := json.Marshal(map[string][]string{"sourceIds": args[1:]})
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			payload = encoded
		}
		return http.MethodPost, "/api/v1/state/export", payload, "", true
	case cmd == "export-get" && len(args) == 3:
		return http.MethodGet, "/api/v1/state/exports/" + url.PathEscape(args[1]), nil, args[2], true
	case cmd == "inventory" && len(args) == 2:
		return http.MethodPost, "/api/v1/sources/" + url.PathEscape(args[1]) + "/inventory", nil, "", true
	case cmd == "gc" && (len(args) == 2 || (len(args) == 3 && args[2] == "execute")):
		execute := "false"
		if len(args) == 3 {
			execute = "true"
		}
		return http.MethodPost, "/api/v1/sources/" + url.PathEscape(args[1]) + "/gc", []byte(`{"execute":` + execute + `}`), "", true
	case cmd == "gc-candidates" && len(args) == 2:
		return http.MethodGet, "/api/v1/maintenance/" + url.PathEscape(args[1]) + "/candidates", nil, "", true
	case cmd == "maintenance" && len(args) == 1:
		return http.MethodGet, "/api/v1/maintenance", nil, "", true
	case cmd == "maintenance" && len(args) == 2:
		return http.MethodGet, "/api/v1/maintenance?sourceId=" + url.QueryEscape(args[1]), nil, "", true
	case cmd == "maintenance-get" && len(args) == 2:
		return http.MethodGet, "/api/v1/maintenance/" + url.PathEscape(args[1]), nil, "", true
	case cmd == "maintenance-cancel" && len(args) == 2:
		return http.MethodPost, "/api/v1/maintenance/" + url.PathEscape(args[1]) + "/cancel", nil, "", true
	default:
		return "", "", nil, "", false
	}
}

func mustRead(path string) []byte {
	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	return data
}

func request(method, rawURL, token string, body []byte, dest string) int {
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, rawURL, r)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	timeout := 30 * time.Second
	if dest != "" {
		timeout = 10 * time.Minute
	}
	c := &http.Client{Timeout: timeout}
	resp, err := c.Do(req)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer resp.Body.Close()
	if dest != "" {
		if resp.StatusCode >= 300 {
			out, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
			fmt.Fprint(os.Stderr, string(out))
			return 1
		}
		if _, err = os.Stat(dest); err == nil {
			fmt.Fprintln(os.Stderr, "destination already exists")
			return 1
		} else if !errors.Is(err, os.ErrNotExist) {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		dir := filepath.Dir(dest)
		if err = os.MkdirAll(dir, 0755); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		tmp, err := os.CreateTemp(dir, ".mirrorctl-download-*")
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		tmpName := tmp.Name()
		ok := false
		defer func() {
			_ = tmp.Close()
			if !ok {
				_ = os.Remove(tmpName)
			}
		}()
		if _, err = io.Copy(tmp, resp.Body); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if err = tmp.Sync(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if err = tmp.Close(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if err = os.Rename(tmpName, dest); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		ok = true
		fmt.Println(dest)
		return 0
	}
	out, _ := io.ReadAll(resp.Body)
	fmt.Print(string(out))
	if resp.StatusCode >= 300 {
		return 1
	}
	return 0
}
