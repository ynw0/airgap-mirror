package server

import (
	"embed"
	"net/http"
)

//go:embed web/index.html web/configure-agent.sh web/configure-apt-signing.sh
var webUIFiles embed.FS

func (a *API) webUI(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	body, err := webUIFiles.ReadFile("web/index.html")
	if err != nil {
		http.Error(w, "管理页面暂时无法加载", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; connect-src 'self'; base-uri 'none'; form-action 'self'")
	_, _ = w.Write(body)
}

func downloadScript(w http.ResponseWriter, path, filename string) {
	body, err := webUIFiles.ReadFile(path)
	if err != nil {
		http.Error(w, "配置脚本暂时无法下载", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=\""+filename+"\"")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(body)
}

func (a *API) downloadAgentConfig(w http.ResponseWriter, _ *http.Request) {
	downloadScript(w, "web/configure-agent.sh", "configure-agent.sh")
}

func (a *API) downloadAPTSigningConfig(w http.ResponseWriter, _ *http.Request) {
	downloadScript(w, "web/configure-apt-signing.sh", "configure-apt-signing.sh")
}
