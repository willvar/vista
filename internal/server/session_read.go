package server

import (
	"encoding/json"
	"net/http"
)

func (a *App) readSession(w http.ResponseWriter, r *http.Request, sessionID string) {
	var input struct {
		Sequence int64 `json:"sequence"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&input); err != nil || input.Sequence <= 0 {
		http.Error(w, "a positive reply sequence is required", http.StatusBadRequest)
		return
	}
	sequence, err := a.store.MarkRead(r.Context(), sessionID, input.Sequence)
	if err != nil {
		http.Error(w, "could not mark session read", http.StatusInternalServerError)
		return
	}
	jsonOut(w, map[string]int64{"readSequence": sequence})
}
