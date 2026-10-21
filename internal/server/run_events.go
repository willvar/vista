package server

import (
	"errors"
	"net/http"
	"strconv"

	"vista/internal/store"
)

func runEventsError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		http.NotFound(w, r)
	case errors.Is(err, store.ErrRunEventRange):
		http.Error(w, "invalid execution event range", http.StatusBadRequest)
	default:
		http.Error(w, "could not read execution events", http.StatusInternalServerError)
	}
}

func (a *App) runEvent(w http.ResponseWriter, r *http.Request, sessionID, runID, rawSequence string) {
	sequence, err := strconv.ParseInt(rawSequence, 10, 64)
	if err != nil || sequence <= 0 {
		http.Error(w, "invalid execution event sequence", http.StatusBadRequest)
		return
	}
	event, err := a.store.RunEvent(r.Context(), sessionID, runID, sequence)
	if err != nil {
		runEventsError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	jsonOut(w, event)
}

func (a *App) runTurnEvents(w http.ResponseWriter, r *http.Request, sessionID, runID, rawTurn string) {
	turn, err := strconv.ParseInt(rawTurn, 10, 32)
	if err != nil || turn <= 0 {
		http.Error(w, "invalid model turn", http.StatusBadRequest)
		return
	}
	var before int64
	if r.URL.Query().Has("before") {
		before, err = strconv.ParseInt(r.URL.Query().Get("before"), 10, 64)
		if err != nil || before <= 0 {
			http.Error(w, "invalid before sequence", http.StatusBadRequest)
			return
		}
	}
	events, err := a.store.RunTurnEvents(r.Context(), sessionID, runID, int32(turn), before)
	if err != nil {
		runEventsError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	compactEventPage(r, events)
	jsonOut(w, events)
}
