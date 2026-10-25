package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"vista/internal/store"
	"vista/internal/workerproto"
)

// Pull current durable data at a decision boundary. If a concurrent edit or
// withdrawal wins, delivery retries from the database instead of stale IPC data.
func (a *App) pullInterjection(ctx context.Context, sessionID, runID string, completed ...*workerproto.CompletedReply) (*workerproto.Command, error) {
	var reply *workerproto.CompletedReply
	if len(completed) > 0 {
		reply = completed[0]
	}
	for {
		input, err := a.store.NextInterjection(ctx, sessionID, runID)
		if err != nil || input == nil {
			return nil, err
		}
		message, err := a.store.DeliverInput(ctx, sessionID, runID, input.ID, uuid.NewString(), storeCompletedReply(reply)...)
		if err != nil {
			return nil, err
		}
		if message == nil {
			continue
		}
		images, err := a.store.MessageResources(ctx, sessionID, message.ID)
		if err != nil {
			return nil, err
		}
		language, err := a.store.ReplyLanguage(ctx, sessionID)
		if err != nil {
			return nil, err
		}
		a.notifySession(sessionID, "sessions")
		return &workerproto.Command{Debug: input.Debug, RunID: runID, InputID: input.ID, MessageSequence: message.Sequence, Prompt: message.Content, ReplyLanguage: language, Images: images}, nil
	}
}

func inputHTTPError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, sql.ErrNoRows):
		http.Error(w, "pending input not found", http.StatusNotFound)
	case errors.Is(err, store.ErrInputConsumed), errors.Is(err, store.ErrClosed):
		http.Error(w, err.Error(), http.StatusConflict)
	default:
		http.Error(w, "could not update pending input", http.StatusInternalServerError)
	}
}

func (a *App) inputQueue(w http.ResponseWriter, r *http.Request, sessionID string, parts []string) {
	if len(parts) == 1 && parts[0] == "resume" && r.Method == http.MethodPost {
		event, err := a.store.ResumeInputs(r.Context(), sessionID)
		if err != nil {
			inputHTTPError(w, err)
			return
		}
		a.notifySession(sessionID, "sessions")
		a.background.Go(func() { a.launchQueuedInput(sessionID) })
		jsonOut(w, map[string]any{"event": event})
		return
	}
	if len(parts) == 0 || len(parts) > 2 {
		http.NotFound(w, r)
		return
	}
	id, err := uuid.Parse(parts[0])
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if len(parts) == 1 && r.Method == http.MethodPatch {
		r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
		var update struct {
			Content *string `json:"content"`
			Mode    *string `json:"mode"`
		}
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if decoder.Decode(&update) != nil || decoder.Decode(new(any)) != io.EOF || update.Content == nil && update.Mode == nil {
			http.Error(w, "invalid input update", http.StatusBadRequest)
			return
		}
		if update.Content != nil {
			trimmed := strings.TrimSpace(*update.Content)
			update.Content = &trimmed
			if len(trimmed) > workerproto.MaxPromptBytes {
				http.Error(w, "input text exceeds 8192 bytes", http.StatusBadRequest)
				return
			}
		}
		if update.Mode != nil && *update.Mode != "now" && *update.Mode != "later" {
			http.Error(w, "mode must be now or later", http.StatusBadRequest)
			return
		}
		input, event, err := a.store.UpdateInput(r.Context(), sessionID, id.String(), update.Content, update.Mode)
		if err != nil {
			inputHTTPError(w, err)
			return
		}
		a.notifySession(sessionID, "sessions")
		jsonOut(w, map[string]any{"input": input, "event": event})
		return
	}
	withdraw := len(parts) == 2 && parts[1] == "withdraw" && r.Method == http.MethodPost
	if !withdraw && !(len(parts) == 1 && r.Method == http.MethodDelete) {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	input, images, event, err := a.store.RemoveInput(r.Context(), sessionID, id.String(), withdraw)
	if err != nil {
		inputHTTPError(w, err)
		return
	}
	a.notifySession(sessionID, "sessions")
	jsonOut(w, map[string]any{"input": input, "images": workerImages(images), "event": event})
}
