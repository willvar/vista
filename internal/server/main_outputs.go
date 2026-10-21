package server

import (
	"net/http"
	"strconv"
)

func (a *App) mainOutputs(w http.ResponseWriter, r *http.Request, sessionID, runID string) {
	var before int64
	if values, present := r.URL.Query()["before"]; present {
		value, err := strconv.ParseInt(values[0], 10, 64)
		if err != nil || len(values) != 1 || value <= 0 {
			http.Error(w, "invalid before sequence", http.StatusBadRequest)
			return
		}
		before = value
	}
	outputs, err := a.store.MainOutputs(r.Context(), sessionID, runID, before)
	if err != nil {
		runEventsError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	jsonOut(w, outputs)
}
