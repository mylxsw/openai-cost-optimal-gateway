package server

import (
	"embed"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/mylxsw/asteria/log"
)

//go:embed dashboard/dist
var dashboardFS embed.FS

// devMode indicates whether to use filesystem instead of embedded files for development
var devMode = os.Getenv("DEV_MODE") == "true"

func newDashboardHandler() http.Handler {
	var fileSystem fs.FS
	var err error

	if devMode {
		// Development mode: use filesystem
		dashboardDir := filepath.Join("internal", "server", "dashboard", "dist")
		if _, err := os.Stat(dashboardDir); err != nil {
			log.Warningf("dashboard directory %s not found: %v", dashboardDir, err)
			return nil
		}
		fileSystem = os.DirFS(dashboardDir)
		log.Infof("Dashboard running in development mode from %s", dashboardDir)
	} else {
		// Production mode: use embedded filesystem
		fileSystem, err = fs.Sub(dashboardFS, "dashboard/dist")
		if err != nil {
			log.Warningf("dashboard assets not available: %v", err)
			return nil
		}
		log.Infof("Dashboard running in production mode from embedded files")
	}

	fileServer := http.FileServer(http.FS(fileSystem))
	stripped := http.StripPrefix("/dashboard", fileServer)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/dashboard" || r.URL.Path == "/dashboard/" {
			serveDashboardIndex(w, r, fileSystem)
			return
		}

		rel := strings.TrimPrefix(r.URL.Path, "/dashboard")
		rel = strings.TrimPrefix(rel, "/")
		if rel == "" {
			serveDashboardIndex(w, r, fileSystem)
			return
		}

		if _, err := fs.Stat(fileSystem, path.Clean(rel)); err != nil {
			serveDashboardIndex(w, r, fileSystem)
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
