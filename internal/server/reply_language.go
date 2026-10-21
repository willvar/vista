package server

import (
	"net/http"

	"vista/internal/workerproto"
)

func (a *App) saveReplyLanguage(w http.ResponseWriter, r *http.Request, sessionID string) bool {
	code := r.Header.Get("X-Vista-Language")
	if code == "" {
		return true
	}
	if workerproto.ReplyLanguageName(code) == "" {
		http.Error(w, "unsupported reply language", http.StatusBadRequest)
		return false
	}
	if err := a.store.SetReplyLanguage(r.Context(), sessionID, code); err != nil {
		http.Error(w, "could not save reply language", http.StatusInternalServerError)
		return false
	}
	return true
}
