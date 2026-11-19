package server

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

func TestManagementPageAndScriptsDoNotExposeProtectedAPI(t *testing.T) {
	handler := (&API{BearerToken: "test-token"}).Handler()
	page := httptest.NewRecorder()
	handler.ServeHTTP(page, httptest.NewRequest(http.MethodGet, "/", nil))
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), "内网服务器") {
		t.Fatalf("management page status=%d body=%q", page.Code, page.Body.String())
	}
	for _, name := range []string{"configure-agent.sh", "configure-apt-signing.sh"} {
		download := httptest.NewRecorder()
		handler.ServeHTTP(download, httptest.NewRequest(http.MethodGet, "/downloads/"+name, nil))
		if download.Code != http.StatusOK || !strings.HasPrefix(download.Body.String(), "#!/usr/bin/env bash") {
			t.Fatalf("script %s status=%d", name, download.Code)
		}
	}
	protected := httptest.NewRecorder()
	handler.ServeHTTP(protected, httptest.NewRequest(http.MethodGet, "/api/v1/sources", nil))
	if protected.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated API status=%d", protected.Code)
	}
}

func TestManagementPageUsesReverseProxyRelativeURLs(t *testing.T) {
	// Backend serves all API routes from the root. Apache strips /mirror-control/
	// before forwarding, so the embedded browser UI must restore that prefix.
	handler := (&API{BearerToken: "test-token"}).Handler()
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("management page status %d", rec.Code)
	}
	body := rec.Body.String()
	for _, wanted := range []string{
		"const basePath=window.location.pathname.replace(/\\/$/,'');",
		"fetch(basePath+path,",
		`href="downloads/configure-agent.sh"`,
		`href="downloads/configure-apt-signing.sh"`,
	} {
		if !strings.Contains(body, wanted) {
			t.Errorf("embedded UI does not support proxy path: missing %q", wanted)
		}
	}
	// Reject absolute script links, which target the wrong path behind Apache.
	if regexp.MustCompile(`href="/downloads/`).MatchString(body) {
		t.Error("download link ignores Apache proxy prefix")
	}
}
