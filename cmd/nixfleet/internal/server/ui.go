package server

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed ui/*
var uiFS embed.FS

// setupUIRoutes adds routes for the web UI
func (s *Server) setupUIRoutes() {
	// Serve static files from embedded FS
	uiSubFS, err := fs.Sub(uiFS, "ui")
	if err != nil {
		// This shouldn't happen with a valid embed
		panic(err)
	}

	fileServer := http.FileServer(http.FS(uiSubFS))

	// Serve UI static files
	s.mux.Handle("GET /ui/", http.StripPrefix("/ui/", fileServer))

	// Redirect root to UI
	s.mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			http.Redirect(w, r, "/ui/", http.StatusFound)
			return
		}
		http.NotFound(w, r)
	})
}
