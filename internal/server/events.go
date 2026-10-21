package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"vista/internal/store"
	"vista/internal/workerproto"
)

type messageWithResults struct {
	store.Message
	ModelResults []modelResultEvent `json:"modelResults,omitempty"`
}

type modelResultEvent struct {
	TaskID          string                  `json:"taskId,omitempty"`
	ParentTaskID    string                  `json:"parentTaskId,omitempty"`
	Purpose         string                  `json:"purpose,omitempty"`
	ReasoningEffort string                  `json:"reasoningEffort,omitempty"`
	Sequence        int64                   `json:"sequence"`
	CreatedAt       string                  `json:"createdAt"`
	RunID           string                  `json:"runId"`
	Turn            int32                   `json:"turn"`
	Depth           int                     `json:"depth"`
	Provider        string                  `json:"provider"`
	ID              string                  `json:"id"`
	FinishReason    string                  `json:"finishReason"`
	Usage           *workerproto.ModelUsage `json:"usage,omitempty"`
	DurationMS      float64                 `json:"durationMs,omitempty"`
}

func (a *App) messagesWithResults(r *http.Request, id string, items []store.Message) ([]messageWithResults, error) {
	out := make([]messageWithResults, len(items))
	runIDs := make([]string, 0, len(items))
	messageIDs := make([]string, 0, len(items))
	runs := make(map[string][]int, len(items))
	for index, item := range items {
		out[index].Message = item
		if item.Role == "assistant" && item.RunID != "" {
			messageIDs = append(messageIDs, item.ID)
			if _, exists := runs[item.RunID]; !exists {
				runIDs = append(runIDs, item.RunID)
			}
			runs[item.RunID] = append(runs[item.RunID], index)
		}
	}
	boundaries, err := a.store.MessageResultBoundaries(r.Context(), id, messageIDs)
	if err != nil {
		return nil, err
	}
	for index := range out {
		out[index].ResultsAfter = boundaries[out[index].ID]
	}
	events, err := a.store.ModelResultEvents(r.Context(), id, runIDs)
	if err != nil {
		return nil, err
	}
	efforts, err := a.store.RunReasoningEfforts(r.Context(), runIDs)
	if err != nil {
		return nil, err
	}
	for _, event := range events {
		var data workerproto.Result
		if err := json.Unmarshal(event.Data, &data); err != nil || data.ModelResult == nil {
			continue
		}
		result := modelResultEvent{TaskID: data.TaskID, ParentTaskID: data.ParentTaskID, Purpose: data.ModelResult.Purpose, Sequence: event.Sequence, CreatedAt: event.CreatedAt, RunID: data.RunID, Turn: data.Turn, Depth: data.Depth, Provider: data.ModelResult.Provider, ID: data.ModelResult.ID, FinishReason: data.ModelResult.FinishReason, Usage: data.ModelResult.Usage, DurationMS: data.ModelResult.DurationMS, ReasoningEffort: data.ModelResult.ReasoningEffort}
		if result.ReasoningEffort == "" {
			effort := efforts[data.RunID]
			if effort != "default" && !strings.HasPrefix(effort, "config:") {
				result.ReasoningEffort = effort
			}
		}
		for _, index := range runs[data.RunID] {
			if result.Sequence > out[index].ResultsAfter && result.Sequence <= out[index].Sequence {
				out[index].ModelResults = append(out[index].ModelResults, result)
			}
		}
	}
	return out, nil
}

type eventHub struct {
	mu          sync.Mutex
	clients     map[string]map[chan struct{}]struct{}
	subscribers int
}

const maxSessionEventStreams = 8
const maxEventStreams = 128
const streamEventPageSize = 16

func (h *eventHub) subscribe(id string) (<-chan struct{}, func(), bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.subscribers >= maxEventStreams || len(h.clients[id]) >= maxSessionEventStreams {
		return nil, nil, false
	}
	if h.clients == nil {
		h.clients = map[string]map[chan struct{}]struct{}{}
	}
	ch := make(chan struct{}, 1)
	if h.clients[id] == nil {
		h.clients[id] = map[chan struct{}]struct{}{}
	}
	h.clients[id][ch] = struct{}{}
	h.subscribers++
	return ch, func() {
		h.mu.Lock()
		if _, subscribed := h.clients[id][ch]; subscribed {
			delete(h.clients[id], ch)
			h.subscribers--
			if len(h.clients[id]) == 0 {
				delete(h.clients, id)
			}
		}
		h.mu.Unlock()
	}, true
}
func (h *eventHub) notify(id string) {
	h.mu.Lock()
	for ch := range h.clients[id] {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
	h.mu.Unlock()
}
func (h *eventHub) subscriberCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.subscribers
}
func parseAfter(r *http.Request) (int64, error) {
	value := r.URL.Query().Get("after")
	if value == "" {
		return 0, nil
	}
	n, err := strconv.ParseInt(value, 10, 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("invalid after sequence")
	}
	return n, nil
}
func (a *App) messages(w http.ResponseWriter, r *http.Request, id string) {
	if _, forward := r.URL.Query()["after"]; forward {
		if r.URL.Query().Has("before") {
			http.Error(w, "before and after cannot be combined", 400)
			return
		}
		after, err := parseAfter(r)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		items, err := a.store.MessagesAfter(r.Context(), id, after, 100)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		withResults, err := a.messagesWithResults(r, id, items)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		jsonOut(w, withResults)
		return
	}
	var before int64
	if raw := r.URL.Query().Get("before"); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || n <= 0 {
			http.Error(w, "invalid before sequence", 400)
			return
		}
		before = n
	}
	items, err := a.store.MessagesBefore(r.Context(), id, before, 100)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	withResults, err := a.messagesWithResults(r, id, items)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	jsonOut(w, withResults)
}
func compactEventPage(r *http.Request, events []store.Event) {
	if r.URL.Query().Get("compact") != "1" {
		return
	}
	for index := range events {
		events[index] = store.CompactToolEvent(events[index])
	}
}

func (a *App) eventPage(w http.ResponseWriter, r *http.Request, id string) {
	if _, exists := r.URL.Query()["after"]; !exists {
		var before int64
		if raw := r.URL.Query().Get("before"); raw != "" {
			n, err := strconv.ParseInt(raw, 10, 64)
			if err != nil || n <= 0 {
				http.Error(w, "invalid before sequence", 400)
				return
			}
			before = n
		}
		items, err := a.store.EventsBefore(r.Context(), id, before, 100)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		compactEventPage(r, items)
		jsonOut(w, items)
		return
	}
	after, err := parseAfter(r)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	items, err := a.store.EventsAfter(r.Context(), id, after, 100)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	compactEventPage(r, items)
	jsonOut(w, items)
}
func (a *App) eventStream(w http.ResponseWriter, r *http.Request, id string) {
	after, err := parseAfter(r)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "stream unsupported", 500)
		return
	}
	wake, unsubscribe, ok := a.hub.subscribe(id)
	if !ok {
		http.Error(w, "too many event streams", http.StatusServiceUnavailable)
		return
	}
	defer unsubscribe()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	controller := http.NewResponseController(w)
	_ = controller.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if _, err := fmt.Fprint(w, ": connected\n\n"); err != nil {
		return
	}
	flusher.Flush()
	for {
		events, err := a.store.EventsAfter(r.Context(), id, after, streamEventPageSize)
		if err != nil {
			return
		}
		for _, event := range events {
			b, err := json.Marshal(event)
			if err != nil {
				return
			}
			_ = controller.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if _, err = fmt.Fprintf(w, "id: %d\nevent: record\ndata: %s\n\n", event.Sequence, b); err != nil {
				return
			}
			after = event.Sequence
		}
		if len(events) > 0 {
			flusher.Flush()
		}
		if len(events) == streamEventPageSize {
			continue
		}
		select {
		case <-r.Context().Done():
			return
		case <-a.ctx.Done():
			return
		case <-wake:
			continue
		case <-time.After(15 * time.Second):
			_ = controller.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if _, err = fmt.Fprint(w, ": keepalive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}
