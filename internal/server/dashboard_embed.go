package server

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"

	"github.com/mylxsw/asteria/log"
)

//go:embed dashboard/dist
var dashboardFS embed.FS

func newDashboardHandler() http.Handler {
	sub, err := fs.Sub(dashboardFS, "dashboard/dist")
	if err != nil {
		log.Warningf("dashboard assets not available: %v", err)
		return nil
	}

	fileServer := http.FileServer(http.FS(sub))
	stripped := http.StripPrefix("/dashboard", fileServer)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/dashboard" || r.URL.Path == "/dashboard/" {
			serveDashboardIndex(w, r, sub)
			return
		}

		rel := strings.TrimPrefix(r.URL.Path, "/dashboard")
		rel = strings.TrimPrefix(rel, "/")
		if rel == "" {
			serveDashboardIndex(w, r, sub)
			return
		}

		if _, err := fs.Stat(sub, path.Clean(rel)); err != nil {
			serveDashboardIndex(w, r, sub)
			return
		}

		stripped.ServeHTTP(w, r)
	})
}

func serveDashboardIndex(w http.ResponseWriter, r *http.Request, sub fs.FS) {
	data, err := fs.ReadFile(sub, "index.html")
	if err != nil {
		http.Error(w, "dashboard not available", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}
