package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"vista/internal/resources"
	"vista/internal/store"
)

type resourceStorageStatus struct {
	Phase        string                 `json:"phase"`
	Action       string                 `json:"action,omitempty"`
	Stats        *store.ResourceStorage `json:"stats,omitempty"`
	DeletedCount int                    `json:"deletedCount"`
	DeletedBytes int64                  `json:"deletedBytes"`
	Error        string                 `json:"error,omitempty"`
}

func (a *App) resourceStorage(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		a.storageMu.Lock()
		result := a.storageStatus
		a.storageMu.Unlock()
		if result.Phase == "" {
			result.Phase = "idle"
		}
		jsonOut(w, result)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method", 405)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1024)
	var input struct {
		Action string `json:"action"`
	}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || decoder.Decode(new(any)) != io.EOF || input.Action != "scan" && input.Action != "clean" {
		http.Error(w, "invalid storage action", 400)
		return
	}
	a.storageMu.Lock()
	if a.storageStatus.Phase != "running" {
		a.storageStatus = resourceStorageStatus{Phase: "running", Action: input.Action, Stats: a.storageStatus.Stats}
		go a.runResourceStorage(input.Action)
	}
	result := a.storageStatus
	a.storageMu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	jsonOut(w, result)
}

func (a *App) runResourceStorage(action string) {
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	var err error
	if action == "clean" {
		var count int
		var freed int64
		count, freed, err = a.store.CleanResources(ctx, func(count int, bytes int64) {
			a.storageMu.Lock()
			a.storageStatus.DeletedCount = count
			a.storageStatus.DeletedBytes = bytes
			a.storageMu.Unlock()
		})
		a.storageMu.Lock()
		a.storageStatus.DeletedCount, a.storageStatus.DeletedBytes = count, freed
		a.storageMu.Unlock()
	}
	stats, scanErr := a.store.ResourceStorage(ctx)
	a.storageMu.Lock()
	defer a.storageMu.Unlock()
	a.storageStatus.Phase = "idle"
	if scanErr == nil {
		a.storageStatus.Stats = &stats
	}
	if err = errors.Join(err, scanErr); err != nil {
		a.storageStatus.Error = err.Error()
	}
}

func (a *App) sessionResource(w http.ResponseWriter, r *http.Request, sessionID, hash string) {
	if r.Method != http.MethodGet || !resources.ValidHash(hash) {
		http.NotFound(w, r)
		return
	}
	ref, err := a.store.SessionResource(r.Context(), sessionID, hash)
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "could not read resource reference", 500)
		return
	}
	images, err := a.store.ResolveImages(r.Context(), []resources.Ref{ref})
	if err != nil {
		http.Error(w, "resource file is unavailable", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", ref.MediaType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(images[0].Data)
}
