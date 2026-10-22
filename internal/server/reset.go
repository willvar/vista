package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"vista/internal/platform"
	"vista/internal/provider"
)

// appHandler keeps the listener and instance lock while replacing a generation
// of user data. No request or background writer may outlive that generation.
type appHandler struct {
	mu          sync.Mutex
	lifecycleMu sync.Mutex
	requests    sync.WaitGroup
	app         *App
	next        http.Handler
	open        func() (*App, error)
	restart     func() (string, error)
	resetting   bool
	unavailable bool
	appClosed   bool // Protected by lifecycleMu.
	closed      bool // Protected by lifecycleMu.
}

func newAppHandler(app *App, open func() (*App, error)) *appHandler {
	return &appHandler{app: app, next: app.handler(), open: open}
}

func (h *appHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/api/reset" {
		h.reset(w, r)
		return
	}
	h.mu.Lock()
	app, next := h.app, h.next
	api := strings.HasPrefix(r.URL.Path, "/api/")
	// Stopping remains available if a reset failed partway through deletion.
	if r.URL.Path == "/api/stop" {
		h.mu.Unlock()
		app.stop(w, r)
		return
	}
	if api && (h.resetting || h.unavailable) {
		h.mu.Unlock()
		http.Error(w, "Vista is resetting; retry after the reset completes", http.StatusServiceUnavailable)
		return
	}
	if api {
		h.requests.Add(1)
	}
	h.mu.Unlock()
	if api {
		defer h.requests.Done()
		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()
		stop := context.AfterFunc(app.ctx, cancel)
		defer stop()
		r = r.WithContext(ctx)
	}
	next.ServeHTTP(w, r)
}

func (h *appHandler) stopApp() error {
	if h.appClosed {
		return nil
	}
	h.app.closeContext()
	h.app.stopAll()
	h.requests.Wait()
	h.app.background.Wait()
	owners, err := h.app.store.WorkerOwners(context.Background())
	if err != nil {
		return err
	}
	// A failed descendant cleanup must not lose its durable ownership record.
	for _, owner := range owners {
		if err := platform.KillOwnedSession(owner.PID, owner.StartTime); err != nil && !errors.Is(err, platform.ErrIdentityMismatch) {
			return fmt.Errorf("stop Worker %s: %w", owner.SessionID, err)
		}
		if err := h.app.store.ClearWorkerOwner(context.Background(), owner); err != nil {
			return err
		}
	}
	_, interruptErr := h.app.store.InterruptRunning(context.Background(), "")
	closeErr := h.app.store.Close()
	h.appClosed = true
	return errors.Join(interruptErr, closeErr)
}

func (h *appHandler) close() {
	h.lifecycleMu.Lock()
	defer h.lifecycleMu.Unlock()
	h.closed = true
	h.mu.Lock()
	h.unavailable = true
	h.mu.Unlock()
	if err := h.stopApp(); err != nil {
		log.Printf("close Vista data: %v", err)
	}
}

func (h *appHandler) reset(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	var input struct {
		Confirm string `json:"confirm"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || decoder.Decode(new(any)) != io.EOF || input.Confirm != "reset-all" {
		http.Error(w, "explicit confirmation is required to reset all Vista data", http.StatusBadRequest)
		return
	}
	h.mu.Lock()
	if h.resetting {
		h.mu.Unlock()
		http.Error(w, "a reset is already in progress", http.StatusConflict)
		return
	}
	h.resetting = true
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		h.resetting = false
		h.mu.Unlock()
	}()
	h.lifecycleMu.Lock()
	defer h.lifecycleMu.Unlock()
	if h.closed {
		http.Error(w, "Vista is stopping", http.StatusServiceUnavailable)
		return
	}
	// Fail before touching data if an internal directory is misconfigured.
	if err := validateResetDirectories(h.app.configDir, h.app.dir); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	h.mu.Lock()
	h.unavailable = true
	h.mu.Unlock()
	if err := h.stopApp(); err != nil {
		http.Error(w, fmt.Sprintf("could not close Vista data; retry reset: %v", err), http.StatusInternalServerError)
		return
	}
	if err := clearUserData(h.app.configDir, h.app.dir); err != nil {
		http.Error(w, fmt.Sprintf("reset incomplete; retry reset: %v", err), http.StatusInternalServerError)
		return
	}
	if err := provider.RestoreBundledOMPCatalog(); err != nil {
		http.Error(w, fmt.Sprintf("could not restore bundled model metadata: %v", err), http.StatusInternalServerError)
		return
	}
	app, err := h.open()
	if err != nil {
		http.Error(w, fmt.Sprintf("could not reopen Vista after reset; retry reset: %v", err), http.StatusInternalServerError)
		return
	}
	address := ""
	if h.restart != nil {
		address, err = h.restart()
		if err != nil {
			app.closeContext()
			app.stopAll()
			app.background.Wait()
			_ = app.store.Close()
			http.Error(w, fmt.Sprintf("could not restart Vista after reset; retry reset: %v", err), http.StatusInternalServerError)
			return
		}
	}
	h.mu.Lock()
	h.app, h.next = app, app.handler()
	h.unavailable = false
	h.mu.Unlock()
	h.appClosed = false
	w.Header().Set("Connection", "close")
	jsonOut(w, struct {
		Reset   bool   `json:"reset"`
		Address string `json:"address,omitempty"`
	}{Reset: true, Address: address})
}

func validateResetDirectories(configDir, stateDir string) error {
	if !filepath.IsAbs(configDir) || filepath.Dir(filepath.Clean(configDir)) == filepath.Clean(configDir) || filepath.Clean(stateDir) != filepath.Join(filepath.Clean(configDir), "state") {
		return errors.New("invalid Vista data directories")
	}
	return nil
}

// Delete entries, rather than following references to external skills/files.
// Keep the live instance lock and address; the open server log is truncated so
// this also works on Windows, where an open log file cannot be removed.
func clearUserData(configDir, stateDir string) error {
	if err := validateResetDirectories(configDir, stateDir); err != nil {
		return err
	}
	root, err := os.OpenRoot(configDir)
	if err != nil {
		return err
	}
	defer root.Close()
	state, err := root.OpenRoot("state")
	if err != nil {
		return err
	}
	defer state.Close()
	if err := removeResetEntries(root, map[string]bool{"state": true}); err != nil {
		return err
	}
	if err := removeResetEntries(state, map[string]bool{"server.lock": true, "instance.json": true, "server.log": true}); err != nil {
		return err
	}
	file, err := state.OpenFile("server.log", os.O_WRONLY|os.O_TRUNC, 0600)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return file.Close()
}

func removeResetEntries(root *os.Root, keep map[string]bool) error {
	dir, err := root.Open(".")
	if err != nil {
		return err
	}
	entries, readErr := dir.ReadDir(-1)
	closeErr := dir.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return err
	}
	for _, entry := range entries {
		if !keep[entry.Name()] {
			if err := root.RemoveAll(entry.Name()); err != nil {
				return err
			}
		}
	}
	return nil
}
