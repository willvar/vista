package server

import (
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"time"

	"vista/internal/store"
)

// A subscription belongs to one live SSE connection. Closing that connection
// releases its interests; no lease, polling or durable client registry is needed.
type stateSubscriber struct {
	id       string
	topics   map[string]bool
	timezone *time.Location
	window   *sessionWindowInterest
}

type sessionWindowInterest struct {
	Size          int               `json:"size"`
	Sort          store.SessionSort `json:"sort"`
	UntitledLabel string            `json:"untitledLabel,omitempty"`
}

func (h *stateHub) clientInterests(id string) (map[string]bool, *time.Location, *sessionWindowInterest) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, client := range h.clients {
		if client.id == id {
			return client.topics, client.timezone, client.window
		}
	}
	return map[string]bool{}, nil, nil
}

func (h *stateHub) interested(topic string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, client := range h.clients {
		if client.topics[topic] {
			return true
		}
	}
	return false
}

func (a *App) stateInterests(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 2048)
	var input struct {
		ID            string                 `json:"id"`
		Topics        []string               `json:"topics"`
		Timezone      string                 `json:"timezone"`
		SessionWindow *sessionWindowInterest `json:"sessionWindow"`
	}
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if d.Decode(&input) != nil || d.Decode(new(any)) != io.EOF || len(input.ID) != 36 || len(input.Topics) > len(stateTopics) || len(input.Timezone) > 100 {
		http.Error(w, "invalid state subscription", http.StatusBadRequest)
		return
	}
	if window := input.SessionWindow; window != nil && (window.Size < 1 || window.Size > store.MaxPageSize || !window.Sort.Valid() || len(window.UntitledLabel) > 200) {
		http.Error(w, "invalid session window", http.StatusBadRequest)
		return
	}
	topics := map[string]bool{"sessions": true, "status": true, "models": true}
	for _, topic := range input.Topics {
		if !slices.Contains(stateTopics, topic) {
			http.Error(w, "invalid state topic", http.StatusBadRequest)
			return
		}
		topics[topic] = true
	}
	var zone *time.Location
	if topics["statistics"] {
		var err error
		zone, err = time.LoadLocation(input.Timezone)
		if err != nil || input.Timezone == "" {
			http.Error(w, "invalid statistics timezone", http.StatusBadRequest)
			return
		}
	}
	found, openedStorage := false, false
	a.updates.mu.Lock()
	for wake, client := range a.updates.clients {
		if client.id != input.ID {
			continue
		}
		found, openedStorage = true, topics["storage"] && !client.topics["storage"]
		client.topics, client.timezone, client.window = topics, zone, input.SessionWindow
		select {
		case wake <- struct{}{}:
		default:
		}
		break
	}
	a.updates.mu.Unlock()
	if !found {
		http.Error(w, "state stream expired", http.StatusNotFound)
		return
	}
	if openedStorage {
		// Reopening reuses a clean cached scan. File/reference changes remain
		// dirty while no view is open, and first use has no cached statistics.
		a.storageMu.Lock()
		a.wakeResourceStorageLocked()
		a.storageMu.Unlock()
	}
	a.cancelUnwatchedStorageScan()
	w.WriteHeader(http.StatusNoContent)
}

// Enumerate at most 61 minutes rather than adding a local hour: this handles
// fractional UTC offsets and skipped/repeated DST hours, including midnight.
func nextStatisticsBoundary(now time.Time, zone *time.Location) time.Time {
	next := now.Truncate(time.Minute).Add(time.Minute)
	for next.In(zone).Minute() != 0 {
		next = next.Add(time.Minute)
	}
	return next
}
