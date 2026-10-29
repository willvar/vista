package server

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"net/http"
	"sync"
	"time"
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
	Event   store.Event     `json:"event"`
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
	message := result.Message
	// Once accepted, completing the local rollback must not depend on a tab
	// staying open. stopWorker drains results and reaps its process tree before
	// truncating history, then a fresh Worker has no discarded in-memory registry.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	a.stopWorker(sessionID)
	_, err = a.store.InterruptRunning(ctx, sessionID, "Stopped for conversation rollback")
	if err != nil {
		return revertedConversation{}, err
	}
	_, err = a.store.InterruptJobs(ctx, sessionID, "Stopped for conversation rollback")
	if err != nil {
		return revertedConversation{}, err
	}
	_, err = a.store.InterruptArtifacts(ctx, sessionID, "Stopped for conversation rollback")
	if err != nil {
		return revertedConversation{}, err
	}
	previousPaths, err := a.store.ArtifactPaths(ctx, sessionID)
	if err != nil {
		return revertedConversation{}, err
	}
	result.Event, err = a.store.Revert(ctx, sessionID, messageID, message.Sequence, retain...)
	if err == nil {
		if paths, readErr := a.store.ArtifactPaths(ctx, sessionID); readErr == nil {
			retained := map[string]bool{}
			for _, path := range paths {
				retained[path] = true
			}
			for _, path := range previousPaths {
				if !retained[path] {
					if removeErr := a.removeOutput(path); removeErr != nil {
						log.Printf("remove reverted output: %v", removeErr)
					}
				}
			}
		}
	}
	a.notifySession(sessionID)
	item, getErr := a.store.Get(ctx, sessionID)
	if getErr == nil && item.Open {
		startErr := a.startWorker(sessionID)
		// The rollback is already committed. Worker availability is conveyed by
		// the usual status endpoint, not a misleading rollback failure.
		if err != nil {
			err = errors.Join(err, startErr)
		}
	}
	return result, err
}

func revertHTTPError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrBusy):
		http.Error(w, err.Error(), http.StatusConflict)
	case errors.Is(err, sql.ErrNoRows), errors.Is(err, store.ErrNotFound):
		http.Error(w, "message no longer exists", http.StatusNotFound)
	default:
		http.Error(w, "Could not roll back conversation: "+err.Error(), http.StatusInternalServerError)
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
