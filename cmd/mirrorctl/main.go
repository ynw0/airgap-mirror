package main

import (
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
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: mirrorctl [flags] health|sources|capacity:<source-id>")
		os.Exit(2)
	}
	cmd := flag.Arg(0)
	path := ""
	switch {
	case cmd == "health":
		path = "/api/v1/health"
	case cmd == "sources":
		path = "/api/v1/sources"
	case strings.HasPrefix(cmd, "capacity:"):
		path = "/api/v1/capacity?sourceId=" + strings.TrimPrefix(cmd, "capacity:")
	default:
		fmt.Fprintln(os.Stderr, "unknown command", cmd)
		os.Exit(2)
	}
	req, _ := http.NewRequest(http.MethodGet, strings.TrimRight(base, "/")+path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	c := &http.Client{Timeout: 30 * time.Second}
	resp, err := c.Do(req)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	fmt.Print(string(body))
	if resp.StatusCode >= 300 {
		os.Exit(1)
	}
}
