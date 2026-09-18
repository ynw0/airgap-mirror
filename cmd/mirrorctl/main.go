package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

func main() {
	var base, token string
	flag.StringVar(&base, "url", "http://127.0.0.1:8787", "Mirror Agent URL")
	flag.StringVar(&token, "token", os.Getenv("AIRGAP_MIRROR_TOKEN"), "Bearer token")
	flag.Parse()
	method, path, body, ok := parseCommand(flag.Args())
	if !ok {
		fmt.Fprintln(os.Stderr, "usage: mirrorctl [flags] health|sources|sources-get <id>|capacity:<id>|sources-create <file.json>|sources-update <id> <file.json>|inventory <id>|gc <id> [execute]|maintenance [source-id]|maintenance-get <job-id>")
		os.Exit(2)
	}
	code := request(method, strings.TrimRight(base, "/")+path, token, body)
	os.Exit(code)
}

func parseCommand(args []string) (method, path string, body []byte, ok bool) {
	if len(args) == 0 {
		return "", "", nil, false
	}
	cmd := args[0]
	switch {
	case cmd == "health" && len(args) == 1:
		return http.MethodGet, "/api/v1/health", nil, true
	case cmd == "sources" && len(args) == 1:
		return http.MethodGet, "/api/v1/sources", nil, true
	case cmd == "sources-get" && len(args) == 2:
		return http.MethodGet, "/api/v1/sources/" + url.PathEscape(args[1]), nil, true
	case strings.HasPrefix(cmd, "capacity:") && len(args) == 1:
		return http.MethodGet, "/api/v1/capacity?sourceId=" + url.QueryEscape(strings.TrimPrefix(cmd, "capacity:")), nil, true
	case cmd == "sources-create" && len(args) == 2:
		return http.MethodPost, "/api/v1/sources", mustRead(args[1]), true
	case cmd == "sources-update" && len(args) == 3:
		return http.MethodPut, "/api/v1/sources/" + url.PathEscape(args[1]), mustRead(args[2]), true
	case cmd == "inventory" && len(args) == 2:
		return http.MethodPost, "/api/v1/sources/" + url.PathEscape(args[1]) + "/inventory", nil, true
	case cmd == "gc" && (len(args) == 2 || (len(args) == 3 && args[2] == "execute")):
		execute := "false"
		if len(args) == 3 {
			execute = "true"
		}
		return http.MethodPost, "/api/v1/sources/" + url.PathEscape(args[1]) + "/gc", []byte(`{"execute":` + execute + `}`), true
	case cmd == "maintenance" && len(args) == 1:
		return http.MethodGet, "/api/v1/maintenance", nil, true
	case cmd == "maintenance" && len(args) == 2:
		return http.MethodGet, "/api/v1/maintenance?sourceId=" + url.QueryEscape(args[1]), nil, true
	case cmd == "maintenance-get" && len(args) == 2:
		return http.MethodGet, "/api/v1/maintenance/" + url.PathEscape(args[1]), nil, true
	default:
		return "", "", nil, false
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

func request(method, rawURL, token string, body []byte) int {
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
	c := &http.Client{Timeout: 30 * time.Second}
	resp, err := c.Do(req)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	fmt.Print(string(out))
	if resp.StatusCode >= 300 {
		return 1
	}
	return 0
}
