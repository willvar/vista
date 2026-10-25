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
	var runIDs, messageIDs []string
	seen := map[string]bool{}
	for _, item := range items {
		if item.Role != "assistant" || item.RunID == "" {
			continue
		}
		messageIDs = append(messageIDs, item.ID)
		if !seen[item.RunID] {
			runIDs = append(runIDs, item.RunID)
			seen[item.RunID] = true
		}
	}
	boundaries, err := a.store.MessageResultBoundaries(r.Context(), id, messageIDs)
	if err != nil {
		return nil, err
	}
	events, err := a.store.ModelResultEvents(r.Context(), id, runIDs)
	if err != nil {
		return nil, err
	}
	efforts, err := a.store.RunReasoningEfforts(r.Context(), runIDs)
	if err != nil {
		return nil, err
	}
	return messagesWithResultData(items, events, boundaries, efforts), nil
}

func messagesWithResultData(items []store.Message, events []store.Event, boundaries map[string]int64, efforts map[string]string) []messageWithResults {
	out := make([]messageWithResults, len(items))
	runs := map[string][]int{}
	for index, item := range items {
		out[index] = messageWithResults{Message: item}
		out[index].ResultsAfter = boundaries[item.ID]
		if item.Role == "assistant" && item.RunID != "" {
			runs[item.RunID] = append(runs[item.RunID], index)
		}
	}
	for _, event := range events {
		var data workerproto.Result
		if json.Unmarshal(event.Data, &data) != nil || data.ModelResult == nil {
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
	return out
}

func (a *App) conversationSnapshot(w http.ResponseWriter, r *http.Request, id string) {
	w.Header().Set("Cache-Control", "no-store")
	snapshot, err := a.store.ConversationSnapshot(r.Context(), id)
	if err != nil {
		http.Error(w, "could not load conversation snapshot", 500)
		return
	}
	messages := messagesWithResultData(snapshot.Messages, snapshot.Results, snapshot.Boundaries, snapshot.Efforts)
	jsonOut(w, struct {
		store.ConversationSnapshot
		Messages []messageWithResults `json:"messages"`
	}{snapshot, messages})
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
	var through int64
	if raw := r.URL.Query().Get("through"); raw != "" {
		through, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || through < after || through <= 0 {
			http.Error(w, "invalid through sequence", 400)
			return
		}
	}
	items, err := a.store.EventsThrough(r.Context(), id, after, through, 100)
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
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	syncDue := true
	writeEvent := func(event store.Event) bool {
		// Explicit coverage detects a lost frame without assuming contiguous
		// database sequences (conversation rollback can legitimately leave gaps).
		b, err := json.Marshal(struct {
			store.Event
			AfterSequence int64 `json:"afterSequence"`
		}{event, after})
		if err != nil {
			return false
		}
		_ = controller.SetWriteDeadline(time.Now().Add(5 * time.Second))
		if _, err = fmt.Fprintf(w, "id: %d\nevent: record\ndata: %s\n\n", event.Sequence, b); err != nil {
			return false
		}
		after = event.Sequence
		return true
	}
	for {
		if syncDue {
			point, err := a.store.Checkpoint(r.Context(), id)
			if err != nil {
				return
			}
			// Drain a fixed committed range before its control frame. New writes
			// are delivered on the next pass and cannot make catch-up endless.
			for after < point.ThroughSequence {
				events, err := a.store.EventsThrough(r.Context(), id, after, point.ThroughSequence, streamEventPageSize)
				if err != nil || len(events) == 0 {
					return
				}
				for _, event := range events {
					if !writeEvent(event) {
						return
					}
				}
				flusher.Flush()
			}
			b, err := json.Marshal(point)
			if err != nil {
				return
			}
			_ = controller.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if _, err = fmt.Fprintf(w, "event: sync\ndata: %s\n\n", b); err != nil {
				return
			}
			flusher.Flush()
			syncDue = false
		}
		events, err := a.store.EventsAfter(r.Context(), id, after, streamEventPageSize)
		if err != nil {
			return
		}
		for _, event := range events {
			if !writeEvent(event) {
				return
			}
			if event.Kind == "run_finished" || event.Kind == "conversation_reverted" {
				syncDue = true
			}
		}
		if len(events) > 0 {
			flusher.Flush()
		}
		select {
		case <-heartbeat.C:
			syncDue = true
		default:
		}
		if len(events) == streamEventPageSize || syncDue {
			continue
		}
		select {
		case <-r.Context().Done():
			return
		case <-a.ctx.Done():
			return
		case <-wake:
		case <-heartbeat.C:
			syncDue = true
		}
	}
}
