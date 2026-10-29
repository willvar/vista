package mcphost

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/uuid"
	"vista/internal/mcpclient"
)

type hostedLease struct {
	mu      sync.Mutex
	closed  bool
	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	entries map[string]*entry
	dir     string
}

func (l *hostedLease) begin() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return false
	}
	l.wg.Add(1)
	return true
}

type hostHandler struct {
	pool   *Pool
	token  string
	root   string
	mu     sync.Mutex
	leases map[string]*hostedLease
}

func (h *hostHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.Header.Get("Origin") != "" || subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+h.token)) != 1 {
		http.Error(w, "private MCP host", http.StatusForbidden)
		return
	}
	var input request
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxControlBytes))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || decoder.Decode(new(any)) != io.EOF {
		http.Error(w, "invalid MCP host request", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if r.URL.Path == "/lease" {
		h.lease(w, r, input)
		return
	}
	h.mu.Lock()
	lease := h.leases[input.Lease]
	if lease == nil || !lease.begin() {
		h.mu.Unlock()
		http.Error(w, "MCP execution connection was interrupted; its call was not replayed", http.StatusGone)
		return
	}
	h.mu.Unlock()
	defer lease.wg.Done()
	e := lease.entries[input.Key]
	if e == nil {
		http.Error(w, "MCP connection is outside this execution snapshot", http.StatusForbidden)
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	stop := context.AfterFunc(lease.ctx, cancel)
	defer stop()
	server, err := h.pool.get(ctx, e, input.Force)
	var data any
	if err == nil {
		switch r.URL.Path {
		case "/list":
			data, err = server.ListTools(ctx, input.Cursor)
		case "/call":
			// Exactly one upstream attempt. Reconnection never replays this call.
			data, err = server.CallTool(ctx, input.Name, input.Arguments)
		case "/inspect":
			data, err = inspect(ctx, server)
			if err == nil {
				h.pool.mu.Lock()
				e.state = State{Status: "available", CheckedAt: time.Now().UTC(), ToolCount: len(data.([]mcpclient.Tool))}
				h.pool.mu.Unlock()
				h.pool.publish(e)
			}
		default:
			err = errors.New("unknown MCP host operation")
		}
	}
	if err != nil {
		_ = json.NewEncoder(w).Encode(response{Error: mcpclient.RedactError(err.Error(), e.definition.Config)})
		return
	}
	encoded, err := json.Marshal(data)
	if err != nil {
		_ = json.NewEncoder(w).Encode(response{Error: "encode MCP result"})
		return
	}
	out := response{Data: encoded}
	if len(encoded) > inlineResultBytes {
		file, err := os.CreateTemp(lease.dir, "result-*.json")
		if err == nil {
			_, err = file.Write(encoded)
			err = errors.Join(err, file.Close())
		}
		if err != nil {
			_ = json.NewEncoder(w).Encode(response{Error: "save MCP output"})
			return
		}
		name, _ := filepath.Rel(h.root, file.Name())
		out = response{File: name, Bytes: int64(len(encoded))}
	}
	_ = json.NewEncoder(w).Encode(out)
}

func (h *hostHandler) lease(w http.ResponseWriter, r *http.Request, input request) {
	entries, release, err := h.pool.retain(input.Definitions)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	defer release()
	id := uuid.NewString()
	dir := filepath.Join(h.pool.directory, "results", id)
	if err := os.MkdirAll(dir, 0700); err != nil {
		http.Error(w, "prepare MCP output directory", 500)
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	lease := &hostedLease{ctx: ctx, cancel: cancel, entries: entries, dir: dir}
	h.mu.Lock()
	h.leases[id] = lease
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		delete(h.leases, id)
		lease.mu.Lock()
		lease.closed = true
		lease.mu.Unlock()
		h.mu.Unlock()
		cancel()
		lease.wg.Wait()
		_ = os.RemoveAll(dir)
	}()
	_ = json.NewEncoder(w).Encode(id)
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
	// This live HTTP connection is the lease: process death/EOF releases all
	// versions and temporary results, including after an abrupt Worker crash.
	<-ctx.Done()
}

func inspect(ctx context.Context, server mcpclient.Connection) ([]mcpclient.Tool, error) {
	tools := []mcpclient.Tool{}
	cursor, seen := "", map[string]bool{}
	for {
		page, err := server.ListTools(ctx, cursor)
		if err != nil {
			return nil, err
		}
		tools = append(tools, page.Tools...)
		if len(tools) > 1024 {
			return nil, errors.New("MCP discovery exceeds 1024 tools")
		}
		if page.NextCursor == "" {
			return tools, nil
		}
		if seen[page.NextCursor] {
			return nil, errors.New("MCP server repeated a tool-list cursor")
		}
		seen[page.NextCursor], cursor = true, page.NextCursor
	}
}
