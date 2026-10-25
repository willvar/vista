package server

import (
	"errors"
	"io/fs"
	"net/http"
	"time"

	"github.com/google/uuid"
	"vista/internal/modeltrace"
)

func requestDebugMode(r *http.Request) bool {
	cookie, err := r.Cookie("vista-debug-mode")
	return err == nil && cookie.Value == "true"
}

func (a *App) cleanModelDiagnostics() {
	modeltrace.Prune(a.dir)
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-a.ctx.Done():
			return
		case <-ticker.C:
			modeltrace.Prune(a.dir)
		}
	}
}

func (a *App) modelDiagnostics(w http.ResponseWriter, r *http.Request, sessionID, runID, id string) {
	w.Header().Set("Cache-Control", "no-store")
	if _, err := uuid.Parse(runID); err != nil {
		http.NotFound(w, r)
		return
	}
	if err := a.store.RequireRun(r.Context(), sessionID, runID); err != nil {
		runEventsError(w, r, err)
		return
	}
	if id == "" {
		records, err := modeltrace.List(a.dir, sessionID, runID)
		if err != nil {
			http.Error(w, "could not read model diagnostics", 500)
			return
		}
		jsonOut(w, records)
		return
	}
	record, err := modeltrace.Read(a.dir, sessionID, runID, id)
	if errors.Is(err, fs.ErrNotExist) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "could not read model diagnostic record", 500)
		return
	}
	jsonOut(w, record)
}
