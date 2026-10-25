package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"vista/internal/modeltrace"
	"vista/internal/store"
)

// Current-state updates carry topic revisions and changed workspace snapshots;
// they are not a second durable conversation log. Initial checkpoints and
// heartbeats repeat revisions so missing payloads can be reconciled by HTTP.
type stateHub struct {
	mu          sync.Mutex
	epoch       string
	revisions   map[string]uint64
	clients     map[chan struct{}]struct{}
	diagnostics []diagnosticRevision
}

const maxStateEventStreams = maxEventStreams

// Leave framing headroom within the shared client's 128,000-byte SSE limit.
// Oversized lists retain revision-driven HTTP snapshot recovery.
const maxStateSnapshotBytes = 120_000

var stateTopics = []string{"sessions", "status", "models", "catalog", "provider-login", "mcp", "storage", "memories", "diagnostics"}

type stateCheckpoint struct {
	Epoch          string                     `json:"epoch"`
	Revisions      map[string]uint64          `json:"revisions"`
	Sessions       *store.SessionListSnapshot `json:"sessions,omitempty"`
	Status         map[string]any             `json:"status,omitempty"`
	Models         *publicModelCatalog        `json:"models,omitempty"`
	Catalog        *modelCatalogInfo          `json:"catalog,omitempty"`
	ProviderLogins *[]loginUpdate             `json:"providerLogins,omitempty"`
	MCP            *mcpStateSnapshot          `json:"mcp,omitempty"`
	Storage        *resourceStorageStatus     `json:"storage,omitempty"`
	Memories       *memorySnapshot            `json:"memories,omitempty"`
	Diagnostics    *diagnosticChanges         `json:"diagnostics,omitempty"`
}

func (a *App) stateUpdate(ctx context.Context, host string, previous stateCheckpoint) (stateCheckpoint, error) {
	// Capture revisions before the reads. A commit during a read must still
	// produce a later revision rather than tag older data with its revision.
	point := a.updates.snapshot()
	if point.Revisions["sessions"] != previous.Revisions["sessions"] {
		// Archives are read only by the session manager. Global state carries
		// open summaries and aggregate counts, regardless of archive size.
		sessions, err := a.store.ListSnapshot(ctx, true)
		if err != nil {
			return stateCheckpoint{}, err
		}
		point.Sessions = &sessions
	}
	if point.Revisions["status"] != previous.Revisions["status"] {
		status, err := a.statusSnapshot(ctx, host)
		if err != nil {
			return stateCheckpoint{}, err
		}
		point.Status = status
	}
	if err := a.addStateSnapshots(ctx, &point, previous); err != nil {
		return stateCheckpoint{}, err
	}
	err := point.limitPayload()
	return point, err
}

func (h *stateHub) snapshot() stateCheckpoint {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.epoch == "" {
		h.epoch = uuid.NewString()
	}
	point := stateCheckpoint{Epoch: h.epoch, Revisions: make(map[string]uint64, len(stateTopics))}
	for _, topic := range stateTopics {
		point.Revisions[topic] = h.revisions[topic]
	}
	return point
}

func (h *stateHub) notify(topics ...string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.notifyLocked(topics...)
}

func (h *stateHub) notifyLocked(topics ...string) {
	if h.revisions == nil {
		h.revisions = make(map[string]uint64)
	}
	for _, topic := range topics {
		h.revisions[topic]++
	}
	for ch := range h.clients {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func (h *stateHub) subscribe() (<-chan struct{}, func(), bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.clients) >= maxStateEventStreams {
		return nil, nil, false
	}
	if h.clients == nil {
		h.clients = make(map[chan struct{}]struct{})
	}
	ch := make(chan struct{}, 1)
	h.clients[ch] = struct{}{}
	return ch, func() { h.mu.Lock(); delete(h.clients, ch); h.mu.Unlock() }, true
}

func (h *stateHub) count() int { h.mu.Lock(); defer h.mu.Unlock(); return len(h.clients) }

func (a *App) stateEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method", 405)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "stream unsupported", 500)
		return
	}
	wake, unsubscribe, ok := a.updates.subscribe()
	if !ok {
		http.Error(w, "too many state streams", 503)
		return
	}
	defer unsubscribe()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	controller := http.NewResponseController(w)
	write := func(kind string, value any) bool {
		data, err := json.Marshal(value)
		if err != nil {
			return false
		}
		_ = controller.SetWriteDeadline(time.Now().Add(5 * time.Second))
		if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", kind, data); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}
	sent := a.updates.snapshot()
	// Initial and reconnect checkpoints use the HTTP snapshot calibration.
	if !write("state", sent) {
		return
	}
	update := func() bool {
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		point, err := a.stateUpdate(ctx, r.Host, sent)
		if err != nil || !write("state", point) {
			return false
		}
		sent = stateCheckpoint{Epoch: point.Epoch, Revisions: point.Revisions}
		return true
	}
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	var metrics <-chan time.Time
	if r.URL.Query().Get("metrics") == "1" {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		metrics = ticker.C
	}
	var shutdown <-chan struct{}
	if a.ctx != nil {
		shutdown = a.ctx.Done()
	}
	for {
		select {
		case <-r.Context().Done():
			return
		case <-shutdown:
			return
		case <-heartbeat.C:
			if !update() {
				return
			}
		case <-wake:
			// Coalesce committed changes into one state update.
			timer := time.NewTimer(100 * time.Millisecond)
			select {
			case <-r.Context().Done():
				timer.Stop()
				return
			case <-shutdown:
				timer.Stop()
				return
			case <-timer.C:
			}
			select {
			case <-wake:
			default:
			}
			if !update() {
				return
			}
		case <-metrics:
			status, err := a.statusSnapshot(r.Context(), r.Host)
			if err != nil || !write("metrics", status) {
				return
			}
		}
	}
}

// Mutation handlers publish after their response and commit. Async operations
// also publish at their state transitions; they never depend on this wakeup.
type mutationResponse struct {
	http.ResponseWriter
	status int
}

func (w *mutationResponse) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}
func (w *mutationResponse) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = 200
	}
	return w.ResponseWriter.Write(b)
}
func (a *App) stateMutations(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet || r.Method == http.MethodHead {
			next.ServeHTTP(w, r)
			return
		}
		response := &mutationResponse{ResponseWriter: w}
		next.ServeHTTP(response, r)
		if response.status >= 400 {
			return
		}
		switch {
		case r.URL.Path == "/api/sessions":
			a.updates.notify("sessions", "status")
		case strings.HasPrefix(r.URL.Path, "/api/sessions/"):
			parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/sessions/"), "/")
			// Execution and queue handlers publish at their commit points,
			// including mutations followed by a failed dispatch. The routes
			// below have no independent commit notification.
			switch {
			case len(parts) == 1 && r.Method == http.MethodDelete:
				a.updates.notify("sessions", "status")
			case len(parts) == 1 && r.Method == http.MethodPatch,
				len(parts) == 2 && parts[1] == "read":
				a.updates.notify("sessions")
			case len(parts) == 2 && (parts[1] == "open" || parts[1] == "close" || parts[1] == "retry"):
				a.updates.notify("sessions", "status")
			}
		case strings.HasPrefix(r.URL.Path, "/api/provider-auth/"):
			a.updates.notify("provider-login")
		case strings.HasPrefix(r.URL.Path, "/api/mcp-"):
			a.updates.notify("mcp")
		case r.URL.Path == "/api/model-catalog":
			a.updates.notify("catalog")
		case r.URL.Path == "/api/resource-storage":
			a.updates.notify("storage")
		}
	})
}

func (a *App) notifySession(id string, topics ...string) {
	a.hub.notify(id)
	if len(topics) == 0 {
		topics = []string{"sessions", "status"}
	}
	a.updates.notify(topics...)
}

// Only lifecycle changes affect the list or runtime counts. Model requests,
// tool steps, artifacts and text remain on the durable conversation stream.
func (a *App) notifyWorkerState(kind string) {
	switch kind {
	case "run_finished", "context_status", "job", "agent_task":
		a.updates.notify("sessions", "status")
	case "input_delivered":
		a.updates.notify("sessions")
	case "memory_changed":
		a.updates.notify("memories")
	}
}

type diagnosticRevision struct {
	Revision uint64
	Record   modeltrace.Summary
}

type diagnosticChanges struct {
	After   uint64               `json:"after"`
	Through uint64               `json:"through"`
	Records []modeltrace.Summary `json:"records"`
}

func (h *stateHub) diagnosticSaved(record modeltrace.Summary) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.notifyLocked("diagnostics")
	h.diagnostics = append(h.diagnostics, diagnosticRevision{h.revisions["diagnostics"], record})
	// This is a bounded transport cache, not another diagnostics database.
	if len(h.diagnostics) > 256 {
		h.diagnostics = append([]diagnosticRevision(nil), h.diagnostics[len(h.diagnostics)-256:]...)
	}
}

func (h *stateHub) diagnosticChanges(after, through uint64) *diagnosticChanges {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.diagnostics) == 0 || h.diagnostics[0].Revision > after+1 {
		return nil
	}
	changes := &diagnosticChanges{After: after, Through: through, Records: []modeltrace.Summary{}}
	for _, item := range h.diagnostics {
		if item.Revision > after && item.Revision <= through {
			changes.Records = append(changes.Records, item.Record)
		}
	}
	if uint64(len(changes.Records)) != through-after {
		return nil
	}
	return changes
}
