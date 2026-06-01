package server

import (
	"embed"
	"net/http"
	"strings"

	"agent-platform/internal/api"
)

//go:embed static/monitor.html static/monitor.css static/monitor.js
var monitorStaticFS embed.FS

func (s *Server) handleMonitorPage(w http.ResponseWriter, r *http.Request) {
	s.serveMonitorStatic(w, r, "monitor.html")
}

func (s *Server) handleMonitorAsset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeJSON(w, http.StatusMethodNotAllowed, api.Failure(http.StatusMethodNotAllowed, "method not allowed"))
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/monitor/")
	switch name {
	case "monitor.css", "monitor.js":
		s.serveMonitorStatic(w, r, name)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) serveMonitorStatic(w http.ResponseWriter, r *http.Request, name string) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeJSON(w, http.StatusMethodNotAllowed, api.Failure(http.StatusMethodNotAllowed, "method not allowed"))
		return
	}
	data, err := monitorStaticFS.ReadFile("static/" + name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	switch {
	case strings.HasSuffix(name, ".html"):
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
	case strings.HasSuffix(name, ".css"):
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
	case strings.HasSuffix(name, ".js"):
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	default:
		w.Header().Set("Content-Type", "application/octet-stream")
	}
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(data)
}
