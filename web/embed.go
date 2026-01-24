// Package web provides embedded static files for the dashboard.
package web

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed dist/*
var distFS embed.FS

// Handler returns an http.Handler that serves the embedded dashboard files.
// It handles SPA routing by serving index.html for paths that don't match static files.
func Handler() http.Handler {
	// Strip "dist" prefix from embedded filesystem
	subFS, err := fs.Sub(distFS, "dist")
	if err != nil {
		panic("failed to create sub filesystem: " + err.Error())
	}

	fileServer := http.FileServer(http.FS(subFS))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path

		// Clean path
		if path == "/" {
			path = "/index.html"
		}

		// Try to open the file to check if it exists
		f, err := subFS.Open(strings.TrimPrefix(path, "/"))
		if err == nil {
			f.Close()
			fileServer.ServeHTTP(w, r)
			return
		}

		// File not found - serve index.html for SPA routing
		// This allows React Router to handle client-side routes
		r.URL.Path = "/"
		fileServer.ServeHTTP(w, r)
	})
}
