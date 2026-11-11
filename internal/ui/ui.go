package ui

import (
	"embed"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"
)

// assets always contains a fallback page. The build preparer copies web/dist
// into this directory before a release build; Go forbids embedding ../../web.
//
//go:embed all:assets
var bundled embed.FS

func Handler() http.Handler { f, _ := fs.Sub(bundled, "assets"); return NewHandler(f) }

func NewHandler(files fs.FS) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" && r.Method != "HEAD" {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if name == "" {
			name = "index.html"
		}
		info, err := fs.Stat(files, name)
		if err != nil || info.IsDir() {
			name = "index.html"
		}
		b, err := fs.ReadFile(files, name)
		if err != nil {
			http.Error(w, "frontend unavailable", 503)
			return
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-cache")
		switch path.Ext(name) {
		case ".html":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
		case ".js":
			w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		case ".css":
			w.Header().Set("Content-Type", "text/css; charset=utf-8")
		default:
			contentType := mime.TypeByExtension(path.Ext(name))
			if contentType == "" {
				contentType = http.DetectContentType(b)
			}
			w.Header().Set("Content-Type", contentType)
		}
		if r.Method == "GET" {
			_, _ = w.Write(b)
		}
	})
}
