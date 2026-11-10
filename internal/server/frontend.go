package server

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"fmt"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strconv"
	"time"
)

// This is also a complete static site: index.html and its hashed assets.
//
//go:embed assets
var webAssets embed.FS

func serveFrontendFile(w http.ResponseWriter, r *http.Request, name string, immutable bool) {
	w.Header().Set("Cache-Control", "no-store")
	if !fs.ValidPath(name) {
		http.NotFound(w, r)
		return
	}
	file := "assets/" + name
	info, err := fs.Stat(webAssets, file)
	if err != nil || info.IsDir() {
		http.NotFound(w, r)
		return
	}
	if contentType := mime.TypeByExtension(path.Ext(name)); contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	if immutable {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "no-cache")
	}
	data, err := webAssets.ReadFile(file)
	if err != nil {
		http.Error(w, "asset unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("ETag", fmt.Sprintf(`"%x"`, sha256.Sum256(data)))
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(data))
}
