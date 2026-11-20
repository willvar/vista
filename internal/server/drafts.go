package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"vista/internal/resources"
	"vista/internal/store"
)

func draftError(w http.ResponseWriter, err error) {
	var size *http.MaxBytesError
	switch {
	case errors.Is(err, store.ErrNotFound):
		http.NotFound(w, nil)
	case errors.Is(err, store.ErrDraftConflict):
		http.Error(w, err.Error(), 409)
	case errors.Is(err, store.ErrDraftImage):
		http.Error(w, err.Error(), 400)
	case errors.As(err, &size):
		http.Error(w, "draft attachment is too large", 413)
	default:
		http.Error(w, "could not save or read the draft", 500)
	}
}

func (a *App) drafts(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	key, rest, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/api/drafts/"), "/")
	if key != store.HomeDraft {
		id, err := uuid.Parse(key)
		if err != nil || id.String() != key {
			http.NotFound(w, r)
			return
		}
	}
	if r.Method == http.MethodGet && strings.HasPrefix(rest, "images/") {
		hash := strings.TrimPrefix(rest, "images/")
		if !resources.ValidHash(hash) {
			http.NotFound(w, r)
			return
		}
		ref, err := a.store.DraftImage(r.Context(), key, hash)
		if err != nil {
			draftError(w, err)
			return
		}
		a.serveResource(w, r, ref)
		return
	}
	if rest == "images" && r.Method == http.MethodPost {
		name := r.URL.Query().Get("name")
		rev, err := strconv.ParseInt(r.URL.Query().Get("revision"), 10, 64)
		if err != nil || rev < 0 || len(name) > 1024 || strings.ContainsAny(name, "\r\n\x00") {
			http.Error(w, "invalid attachment", 400)
			return
		}
		if err := a.admitHeavy(); err != nil {
			http.Error(w, err.Error(), 503)
			return
		}
		value, err := a.store.UploadDraftImage(r.Context(), key, rev, name, r.Header.Get("Content-Type"), http.MaxBytesReader(w, r.Body, resources.MaxImageBytes))
		if err != nil {
			draftError(w, err)
			return
		}
		a.updates.notify("drafts")
		jsonOut(w, value)
		return
	}
	if rest != "" {
		http.NotFound(w, r)
		return
	}
	var value store.Draft
	var err error
	switch r.Method {
	case http.MethodGet:
		value, err = a.store.Draft(r.Context(), key)
	case http.MethodPut:
		if decodeSettingsRequest(http.MaxBytesReader(w, r.Body, 256<<10), &value) != nil || value.Revision < 0 || len(value.Text) > 128<<10 || len(value.Quote) > 64<<10 || len(value.Images) > 32 || !validDraftQuote(value.Quote) {
			http.Error(w, "invalid draft", 400)
			return
		}
		for _, ref := range value.Images {
			if !resources.ValidHash(ref.Hash) || !resources.ValidImageType(ref.MediaType) || ref.Bytes < 1 || ref.Bytes > resources.MaxImageBytes || len(ref.Name) > 1024 || strings.ContainsAny(ref.Name, "\r\n\x00") {
				http.Error(w, "invalid draft attachment", 400)
				return
			}
		}
		value, err = a.store.SaveDraft(r.Context(), key, value)
		if err == nil {
			a.updates.notify("drafts")
		}
	default:
		http.Error(w, "method", 405)
		return
	}
	if err != nil {
		draftError(w, err)
		return
	}
	jsonOut(w, value)
}

func validDraftQuote(raw json.RawMessage) bool {
	if len(raw) == 0 || string(raw) == "null" {
		return true
	}
	var q struct {
		Items []struct {
			ID         int64
			Text, Role string
		}
		NextID int64
	}
	if json.Unmarshal(raw, &q) != nil || q.Items == nil || len(q.Items) > 64 || q.NextID < 1 || q.NextID >= 1<<53 {
		return false
	}
	var last int64
	for _, item := range q.Items {
		if item.ID <= last || strings.TrimSpace(item.Text) == "" || item.Role != "user" && item.Role != "assistant" {
			return false
		}
		last = item.ID
	}
	return q.NextID > last
}
