package ui

import (
	"bytes"
	"embed"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"
	"time"
)

//go:embed dist/*
var files embed.FS

func Handler() http.Handler {
	root, _ := fs.Sub(files, "dist")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requested := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if requested == "." || requested == "" {
			requested = "index.html"
		}
		if _, err := fs.Stat(root, requested); err != nil {
			requested = "index.html"
		}
		if ext := path.Ext(requested); ext != "" {
			if kind := mime.TypeByExtension(ext); kind != "" {
				w.Header().Set("Content-Type", kind)
			}
		}
		contents, err := fs.ReadFile(root, requested)
		if err != nil {
			http.Error(w, "interface asset unavailable", http.StatusInternalServerError)
			return
		}
		if strings.HasPrefix(requested, "assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		http.ServeContent(w, r, requested, time.Time{}, bytes.NewReader(contents))
	})
}
