package server

import (
	"errors"
	"net/http"
	"strconv"

	"vista/internal/store"
)

func (a *App) runOverview(w http.ResponseWriter, r *http.Request, sessionID, runID string) {
	var after, through int64
	for name, target := range map[string]*int64{"after": &after, "through": &through} {
		if values, present := r.URL.Query()[name]; present {
			value, err := strconv.ParseInt(values[0], 10, 64)
			if err != nil || len(values) != 1 || value < 0 || name == "through" && value == 0 {
				http.Error(w, "invalid execution overview range", http.StatusBadRequest)
				return
			}
			*target = value
		}
	}
	if through > 0 && after >= through {
		http.Error(w, "invalid execution overview range", http.StatusBadRequest)
		return
	}
	overview, err := a.store.RunOverviewRange(r.Context(), sessionID, runID, after, through)
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "could not read execution overview", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	jsonOut(w, overview)
}
