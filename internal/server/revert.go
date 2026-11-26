package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"sync"

	"github.com/google/uuid"
	"vista/internal/resources"
	"vista/internal/store"
)

func (a *App) lockSessionMutation(id string) func() {
	value, _ := a.sessionMutations.LoadOrStore(id, &sync.Mutex{})
	mu := value.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

type revertedImage = resources.Ref
type revertedConversation struct {
	Message store.Message   `json:"message"`
	Images  []revertedImage `json:"images"`
	Session *store.Session  `json:"session,omitempty"`
	store.HistoryChange
}

func (a *App) messageDraft(ctx context.Context, sessionID, messageID string) (revertedConversation, error) {
	message, images, change, err := a.store.HistoryDraft(ctx, sessionID, messageID)
	return revertedConversation{Message: message, Images: append([]revertedImage{}, images...), HistoryChange: change}, err
}

func (a *App) revertConversation(ctx context.Context, sessionID, messageID string, change store.HistoryChange, retain ...resources.Ref) (revertedConversation, error) {
	result, err := a.messageDraft(ctx, sessionID, messageID)
	if err != nil {
		return result, err
	}
	if !change.WantsBranch() {
		_, err = a.store.WithdrawInPlace(ctx, sessionID, messageID, change)
		if err == nil {
			a.notifySession(sessionID, "sessions")
		}
		return result, err
	}
	branch, err := a.store.ForkForChange(ctx, sessionID, uuid.NewString(), messageID, change, retain...)
	if err != nil {
		return result, err
	}
	item, err := a.store.Get(ctx, branch.ID)
	if err != nil {
		return result, err
	}
	result.Session = &item
	a.notifySession(sessionID, "sessions")
	if branch.RootID != sessionID {
		a.notifySession(branch.RootID)
	}
	if item.Open {
		_ = a.startWorker(branch.ID)
	}
	return result, nil
}

func revertHTTPError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrBusy), errors.Is(err, store.ErrBranchRunning), errors.Is(err, store.ErrClosed), errors.Is(err, store.ErrHistoryChanged), errors.Is(err, store.ErrHistoryBusy):
		http.Error(w, err.Error(), http.StatusConflict)
	case errors.Is(err, store.ErrNotFound):
		http.Error(w, "history no longer exists", http.StatusNotFound)
	default:
		http.Error(w, "Could not change conversation history: "+err.Error(), http.StatusInternalServerError)
	}
}

func (a *App) revertMessage(w http.ResponseWriter, r *http.Request, sessionID, messageID string) {
	var change store.HistoryChange
	r.Body = http.MaxBytesReader(w, r.Body, 1024)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	// An empty legacy request still means fork, not a replacement of history.
	if err := decoder.Decode(&change); err != io.EOF && (err != nil || decoder.Decode(new(any)) != io.EOF) {
		http.Error(w, "invalid history change", http.StatusBadRequest)
		return
	}
	result, err := a.revertConversation(r.Context(), sessionID, messageID, change)
	if err != nil {
		revertHTTPError(w, err)
		return
	}
	jsonOut(w, result)
}

func (a *App) historyVersions(w http.ResponseWriter, r *http.Request, sessionID string, tail []string) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == http.MethodGet && len(tail) == 0 {
		var before int64
		if cursor := r.URL.Query().Get("before"); cursor != "" {
			var err error
			before, err = strconv.ParseInt(cursor, 10, 64)
			if err != nil || before < 1 {
				http.Error(w, "invalid history cursor", 400)
				return
			}
		}
		page, err := a.store.HistoryVersions(r.Context(), sessionID, before)
		if err != nil {
			revertHTTPError(w, err)
			return
		}
		jsonOut(w, page)
		return
	}
	if len(tail) != 1 {
		http.NotFound(w, r)
		return
	}
	version, err := strconv.ParseInt(tail[0], 10, 64)
	if err != nil || version < 1 {
		http.NotFound(w, r)
		return
	}
	switch r.Method {
	case http.MethodGet:
		preview, err := a.store.PreviewHistoryVersion(r.Context(), sessionID, version)
		if err != nil {
			revertHTTPError(w, err)
			return
		}
		jsonOut(w, preview)
	case http.MethodPost:
		var input struct {
			ThroughSequence *int64 `json:"throughSequence"`
		}
		if !decodeSelectionBody(w, r, &input) {
			return
		}
		if _, err := a.store.RestoreHistoryVersion(r.Context(), sessionID, version, input.ThroughSequence); err != nil {
			revertHTTPError(w, err)
			return
		}
		a.notifySession(sessionID, "sessions")
		w.WriteHeader(http.StatusNoContent)
	default:
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method", http.StatusMethodNotAllowed)
	}
}
