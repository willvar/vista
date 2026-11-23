package server

import (
	"errors"
	"log"
	"net/http"
	"sync"

	"vista/internal/store"
)

func (a *App) changeConversation(w http.ResponseWriter, r *http.Request, id string, deleting bool) {
	writeError := func(err error) {
		switch {
		case errors.Is(err, store.ErrNotFound):
			http.NotFound(w, r)
		case errors.Is(err, store.ErrConversationChanged):
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			jsonOut(w, map[string]string{"code": "conversation_changed", "message": "The conversation changed. Try again."})
		default:
			log.Printf("conversation lifecycle change: %v", err)
			http.Error(w, "could not change conversation", http.StatusInternalServerError)
		}
	}
	ctx := r.Context()
	members, err := a.store.ConversationMembers(ctx, id)
	if err != nil {
		writeError(err)
		return
	}
	// A request must not hold the selected session lock before taking this
	// ordered set; any member can also be the target of a single-session edit.
	for _, member := range members {
		defer a.lockSessionMutation(member)()
	}
	if err := a.store.CloseConversation(ctx, id, members); err != nil {
		writeError(err)
		return
	}
	// As with ordinary deletion, close admission before stopping the Workers.
	// Once committed, finish the stop/delete even if the requesting tab leaves.
	var stopped sync.WaitGroup
	for _, member := range members {
		stopped.Go(func() { a.stopWorker(member) })
	}
	stopped.Wait()
	defer func() {
		for _, member := range members {
			a.hub.notify(member)
		}
		a.updates.notify("sessions", "status")
	}()
	if deleting {
		outputs, err := a.store.DeleteConversation(a.ctx, id, members)
		if err != nil {
			writeError(err)
			return
		}
		for _, member := range members {
			a.removeSessionOutputs(member, outputs[member])
		}
		a.invalidateResourceStorage()
	}
	w.Header().Set("Cache-Control", "no-store")
	jsonOut(w, struct {
		SessionIDs []string `json:"sessionIds"`
	}{members})
}
