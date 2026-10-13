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
	return HandlerWithHeadMarkup(nil)
}

// HandlerWithHeadMarkup adds trusted, request-specific markup to the HTML
// entrypoint. Assets remain unchanged, and modified HTML is never cached.
func HandlerWithHeadMarkup(head func(*http.Request) []byte) http.Handler {
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
			if ext == ".webmanifest" {
				w.Header().Set("Content-Type", "application/manifest+json")
			} else if kind := mime.TypeByExtension(ext); kind != "" {
				w.Header().Set("Content-Type", kind)
			}
		}
		contents, err := fs.ReadFile(root, requested)
		if err != nil {
			http.Error(w, "interface asset unavailable", http.StatusInternalServerError)
			return
		}
		if requested == "index.html" && head != nil {
			contents = bytes.Replace(contents, []byte("</head>"), append(head(r), []byte("</head>")...), 1)
		}
		if strings.HasPrefix(requested, "assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else if requested == "index.html" && head != nil {
			w.Header().Set("Cache-Control", "no-store")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		http.ServeContent(w, r, requested, time.Time{}, bytes.NewReader(contents))
	})
}
