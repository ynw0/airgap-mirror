package server

import (
	"net/http"
	"net/http/httptest"
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
