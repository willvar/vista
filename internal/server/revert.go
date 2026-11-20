package server

import (
	"context"
	"errors"
	"net/http"
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
}

func (a *App) messageDraft(requestCtx context.Context, sessionID, messageID string) (revertedConversation, error) {
	message, err := a.store.RevertTarget(requestCtx, sessionID, messageID)
	if err != nil {
		return revertedConversation{}, err
	}
	images, err := a.store.MessageResources(requestCtx, sessionID, messageID)
	if err != nil {
		return revertedConversation{}, err
	}
	result := revertedConversation{Message: message, Images: []revertedImage{}}
	for _, image := range images {
		result.Images = append(result.Images, image)
	}
	return result, nil
}

func (a *App) revertConversation(requestCtx context.Context, sessionID, messageID string, retain ...resources.Ref) (revertedConversation, error) {
	result, err := a.messageDraft(requestCtx, sessionID, messageID)
	if err != nil {
		return revertedConversation{}, err
	}
	branch, err := a.store.Fork(requestCtx, sessionID, uuid.NewString(), messageID, retain...)
	if err != nil {
		return result, err
	}
	item, err := a.store.Get(requestCtx, branch.ID)
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
	case errors.Is(err, store.ErrBusy), errors.Is(err, store.ErrBranchRunning):
		http.Error(w, err.Error(), http.StatusConflict)
	case errors.Is(err, store.ErrNotFound):
		http.Error(w, "message no longer exists", http.StatusNotFound)
	default:
		http.Error(w, "Could not create conversation branch: "+err.Error(), http.StatusInternalServerError)
	}
}

func (a *App) revertMessage(w http.ResponseWriter, r *http.Request, sessionID, messageID string) {
	result, err := a.revertConversation(r.Context(), sessionID, messageID)
	if err != nil {
		revertHTTPError(w, err)
		return
	}
	jsonOut(w, result)
}
