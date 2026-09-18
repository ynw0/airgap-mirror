package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"net/http"
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
		fmt.Fprintln(os.Stderr, "usage: mirrorctl [flags] health|sources|capacity:<source-id>|sources-create <file.json>|sources-update <source-id> <file.json>")
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
	case strings.HasPrefix(cmd, "capacity:") && len(args) == 1:
		return http.MethodGet, "/api/v1/capacity?sourceId=" + strings.TrimPrefix(cmd, "capacity:"), nil, true
	case cmd == "sources-create" && len(args) == 2:
		data, err := os.ReadFile(args[1])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return http.MethodPost, "/api/v1/sources", data, true
	case cmd == "sources-update" && len(args) == 3:
		data, err := os.ReadFile(args[2])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return http.MethodPut, "/api/v1/sources/" + args[1], data, true
	default:
		return "", "", nil, false
	}
}

func request(method, url, token string, body []byte) int {
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, url, r)
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
