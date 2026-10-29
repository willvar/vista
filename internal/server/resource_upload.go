package server

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"vista/internal/resources"
	"vista/internal/store"
	"vista/internal/workerproto"
)

func (a *App) uploadResource(w http.ResponseWriter, r *http.Request, sessionID string) {
	if len(r.URL.Query().Get("name")) > 1024 || !resources.ValidImageType(r.Header.Get("Content-Type")) {
		http.Error(w, "upload requires an image content type and a name of at most 1024 bytes", http.StatusBadRequest)
		return
	}
	item, err := a.store.Get(r.Context(), sessionID)
	if err != nil || !item.Open {
		http.Error(w, "open conversation required", http.StatusConflict)
		return
	}
	if err = a.admitHeavy(); err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	a.mu.Lock()
	worker := a.workers[sessionID]
	ready := workerCanReceive(worker)
	a.mu.Unlock()
	if !ready {
		http.Error(w, "Worker is unavailable", 503)
		return
	}
	dir := filepath.Join(a.dir, "inputs")
	if err = os.MkdirAll(dir, 0700); err != nil {
		http.Error(w, "could not stage upload", 500)
		return
	}
	file, err := os.CreateTemp(dir, "image-*")
	if err != nil {
		http.Error(w, "could not stage upload", 500)
		return
	}
	defer os.Remove(file.Name())
	defer file.Close()
	r.Body = http.MaxBytesReader(w, r.Body, resources.MaxImageBytes)
	if _, err = io.Copy(file, r.Body); err != nil {
		http.Error(w, "image upload failed or exceeds 50 MiB", http.StatusRequestEntityTooLarge)
		return
	}
	if err = file.Close(); err != nil {
		http.Error(w, "could not save upload", 500)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
	defer cancel()
	result, err := worker.rpc(ctx, workerproto.Command{Action: "import_resource", SessionID: sessionID, ResourcePath: file.Name(), ResourceName: r.URL.Query().Get("name"), ResourceType: r.Header.Get("Content-Type")})
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	jsonOut(w, result.Resource)
}

// Resource responses use bounded hashing/copying. Holding the library lock
// protects files from manual cleanup while a response is in progress.
func (a *App) serveResource(w http.ResponseWriter, r *http.Request, ref resources.Ref) {
	library := resources.Library{StateDir: a.dir}
	unlock, err := library.Lock(r.Context())
	if err != nil {
		http.Error(w, "resource unavailable", 404)
		return
	}
	defer unlock()
	file, err := library.Open(r.Context(), ref)
	if err != nil {
		http.Error(w, "resource unavailable", 404)
		return
	}
	defer file.Close()
	w.Header().Set("Content-Type", ref.MediaType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	http.ServeContent(w, r, ref.Name, time.Time{}, file)
}

func (c *workerResultConsumer) importResource(result workerproto.Result) error {
	if result.Resource == nil || result.AckID == "" {
		return store.ErrNotFound
	}
	var source *workerproto.Result
	if result.RPCID == "" {
		source = &result
	}
	err := c.store.AttachResource(c.ctx, c.sessionID, *result.Resource, source)
	ack := workerproto.Command{Action: "ack", RunID: result.RunID, AckID: result.AckID}
	if err != nil {
		ack.Error = err.Error()
	}
	return c.send(ack)
}
