package status

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
)

//go:embed ui/*
var uiFS embed.FS

var uiFiles = map[string]string{
	"app.css":     "text/css; charset=utf-8",
	"app.js":      "text/javascript; charset=utf-8",
	"favicon.svg": "image/svg+xml",
}

func (s *Server) uiIndex(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.NotFound(w, r)
		return
	}
	b, err := uiFS.ReadFile("ui/index.html")
	if err != nil {
		http.Error(w, "ui missing", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(b)
	}
}

func (s *Server) uiFile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.NotFound(w, r)
		return
	}
	name := path.Base(r.PathValue("file"))
	ct, ok := uiFiles[name]
	if !ok {
		http.NotFound(w, r)
		return
	}
	b, err := fs.ReadFile(uiFS, "ui/"+name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(b)
	}
}
