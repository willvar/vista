package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

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

func (a *App) resourceStorageSnapshot() resourceStorageStatus {
	a.storageMu.Lock()
	defer a.storageMu.Unlock()
	result := a.storageStatus
	if result.Phase == "" {
		result.Phase = "idle"
	}
	return result
}

func (a *App) resourceStorage(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		jsonOut(w, a.resourceStorageSnapshot())
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
		a.background.Go(func() { a.runResourceStorage(input.Action) })
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
	a.runResourceStorageContext(ctx, action)
}

func (a *App) runResourceStorageContext(ctx context.Context, action string) {
	defer a.updates.notify("storage")
	a.storageMu.Lock()
	generation := a.storageGeneration
	a.storageMu.Unlock()
	var err error
	if action == "clean" {
		var count int
		var freed int64
		count, freed, err = a.store.CleanResources(ctx, func(count int, bytes int64) {
			a.storageMu.Lock()
			a.storageStatus.DeletedCount = count
			a.storageStatus.DeletedBytes = bytes
			a.storageMu.Unlock()
			a.updates.notify("storage")
		})
		a.storageMu.Lock()
		a.storageStatus.DeletedCount, a.storageStatus.DeletedBytes = count, freed
		a.storageMu.Unlock()
	}
	stats, scanErr := a.store.ResourceStorage(ctx)
	a.storageMu.Lock()
	defer a.storageMu.Unlock()
	a.storageStatus.Phase = "idle"
	if !errors.Is(scanErr, context.Canceled) {
		a.storageScanned = generation
	}
	if scanErr == nil {
		a.storageStatus.Stats = &stats
	}
	if err = errors.Join(err, scanErr); err != nil && !errors.Is(err, context.Canceled) {
		a.storageStatus.Error = err.Error()
	}
	// Changes committed while scanning must not be acknowledged by that scan.
	if a.storageGeneration != generation {
		a.wakeResourceStorageLocked()
	}
}

func (a *App) cancelUnwatchedStorageScan() {
	if a.updates.interested("storage") {
		return
	}
	a.storageMu.Lock()
	if a.storageScanCancel != nil {
		a.storageScanCancel()
	}
	a.storageMu.Unlock()
}

func (a *App) wakeResourceStorageLocked() {
	select {
	case a.storageWake <- struct{}{}:
	default:
	}
}

func (a *App) invalidateResourceStorage() {
	a.storageMu.Lock()
	a.storageGeneration++
	a.wakeResourceStorageLocked()
	a.storageMu.Unlock()
}

// Dirty generations coalesce imports/deletions and filesystem bursts into a
// single scan for all viewers. No viewers means no automatic directory scans.
func (a *App) syncResourceStorage() {
	for {
		select {
		case <-a.ctx.Done():
			return
		case <-a.storageWake:
		}
		timer := time.NewTimer(200 * time.Millisecond)
		select {
		case <-a.ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		if !a.updates.interested("storage") {
			continue
		}
		a.storageMu.Lock()
		needed := (a.storageStatus.Stats == nil || a.storageGeneration != a.storageScanned) && a.storageStatus.Phase != "running"
		ctx, cancel := context.WithCancel(a.ctx)
		if needed {
			// Background accounting must preserve the latest explicit cleanup
			// result, including the file-change notification caused by cleanup.
			a.storageStatus.Phase, a.storageStatus.Action, a.storageStatus.Error = "running", "scan", ""
			a.storageScanCancel = cancel
		}
		a.storageMu.Unlock()
		if needed {
			// The final viewer may have left between the interest check and
			// installing the cancellation owner above.
			a.cancelUnwatchedStorageScan()
			a.updates.notify("storage")
			a.runResourceStorageContext(ctx, "scan")
			a.storageMu.Lock()
			a.storageScanCancel = nil
			a.storageMu.Unlock()
		}
		cancel()
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
	a.serveResource(w, r, ref)
}
