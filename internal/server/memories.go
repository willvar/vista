package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/google/uuid"
	"vista/internal/store"
	"vista/internal/workerproto"
)

func (a *App) memories(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	switch r.Method {
	case http.MethodGet:
	case http.MethodPost:
		r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
		var change workerproto.MemoryChange
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&change); err != nil {
			memoryHTTPError(w, store.ErrInvalidMemory)
			return
		}
		if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
			memoryHTTPError(w, store.ErrInvalidMemory)
			return
		}
		if change.Action == "add" {
			if change.ID != "" {
				memoryHTTPError(w, store.ErrInvalidMemory)
				return
			}
			change.ID = uuid.NewString()
		}
		if err := a.store.ChangeMemory(r.Context(), change); err != nil {
			memoryHTTPError(w, err)
			return
		}
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	items, err := a.store.Memories(r.Context())
	if err != nil {
		memoryHTTPError(w, err)
		return
	}
	jsonOut(w, map[string]any{"memories": items, "maxCount": store.MaxMemories, "maxBytes": store.MaxMemoryBytes, "maxTotalBytes": store.MaxMemoriesBytes})
}

func memoryHTTPError(w http.ResponseWriter, err error) {
	status, code := http.StatusInternalServerError, "memory_unavailable"
	switch {
	case errors.Is(err, store.ErrInvalidMemory):
		status, code = http.StatusBadRequest, "memory_invalid"
	case errors.Is(err, store.ErrMemoryChanged):
		status, code = http.StatusConflict, "memory_changed"
	case errors.Is(err, store.ErrMemoryExists):
		status, code = http.StatusConflict, "memory_exists"
	case errors.Is(err, store.ErrMemoryLimit):
		status, code = http.StatusBadRequest, "memory_limit"
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	jsonOut(w, map[string]string{"code": code, "message": code})
}

func (c *workerResultConsumer) memoryChange(result workerproto.Result) error {
	if result.RunID == "" || result.Depth != 0 || result.TaskID != "" && result.TaskID != "main" || result.Memory == nil || result.AckID == "" || len(result.AckID) > 64 {
		return errors.New("invalid memory change request")
	}
	event, err := c.store.ChangeRunMemory(c.ctx, c.sessionID, result.RunID, *result.Memory)
	if err != nil {
		// Conflicts are tool failures the model can recover from, rather than
		// protocol failures that would terminate the entire session Worker.
		return c.send(workerproto.Command{Action: "ack", RunID: result.RunID, AckID: result.AckID, Error: err.Error()})
	}
	return c.persisted(result, event, nil, result.AckID)
}
