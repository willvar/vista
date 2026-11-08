package server

import (
	"context"
	"errors"
	"net"
	"net/http"
	"time"

	"vista/internal/configstore"
	"vista/internal/platform"
)

// ReservedPortRestart owns a successor which has already bound the new socket.
// Commit arms it to wait for this instance's lock; Abort releases an uncommitted
// reservation. The successor keeps its socket through startup on every OS.
type ReservedPortRestart struct {
	Commit func() error
	Abort  func()
}

type PortReservationError struct {
	Code string
	Err  error
}

func (e *PortReservationError) Error() string { return e.Err.Error() }
func (e *PortReservationError) Unwrap() error { return e.Err }

type RunOptions struct {
	Listener           net.Listener
	WaitForInstance    bool
	ReservePortRestart func(int) (*ReservedPortRestart, error)
}

func portConfigError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	jsonOut(w, map[string]any{"code": code, "message": message})
}

func (h *appHandler) changePort(w http.ResponseWriter, r *http.Request, config RuntimeConfig, revision string, confirmed bool) {
	if !confirmed {
		portConfigError(w, http.StatusUnprocessableEntity, "port_restart_confirmation_required", "Changing the listening port requires confirmation to restart Vista")
		return
	}
	// This callback is an accounted API request. Never wait behind a reset
	// which may already hold lifecycleMu while draining those requests.
	if !h.lifecycleMu.TryLock() {
		portConfigError(w, http.StatusConflict, "lifecycle_in_progress", "A reset or restart is already in progress")
		return
	}
	defer h.lifecycleMu.Unlock()
	h.mu.Lock()
	if h.closed || h.resetting || h.restarting || h.unavailable {
		h.mu.Unlock()
		portConfigError(w, http.StatusConflict, "lifecycle_in_progress", "A reset or restart is already in progress")
		return
	}
	h.restarting = true
	app := h.app
	h.mu.Unlock()
	accepted := false
	defer func() {
		if !accepted {
			h.mu.Lock()
			h.restarting = false
			h.mu.Unlock()
		}
	}()
	if app.ctx.Err() != nil || h.reservePortRestart == nil {
		portConfigError(w, http.StatusServiceUnavailable, "port_restart_unavailable", "Port-change restart is unavailable")
		return
	}
	app.serverConfigMu.Lock()
	defer app.serverConfigMu.Unlock()
	old, present, err := configstore.Read(app.configDir, configstore.Server)
	if err != nil {
		portConfigError(w, 500, "configuration_read_failed", "Could not read server configuration")
		return
	}
	if configstore.Revision(old, present) != revision {
		portConfigError(w, 409, "configuration_changed", errServerConfigChanged.Error())
		return
	}
	reservation, err := h.reservePortRestart(config.Port)
	if err != nil {
		code := "port_restart_start_failed"
		var failure *PortReservationError
		if errors.As(err, &failure) {
			code = failure.Code
		}
		portConfigError(w, http.StatusUnprocessableEntity, code, err.Error())
		return
	}
	defer func() {
		if !accepted {
			reservation.Abort()
		}
	}()
	if r.Context().Err() != nil {
		return
	}
	saved, err := writeServerConfig(app.configDir, config, revision)
	if err != nil {
		if errors.Is(err, configstore.ErrConflict) {
			portConfigError(w, 409, "configuration_changed", errServerConfigChanged.Error())
		} else {
			portConfigError(w, 500, "configuration_save_failed", "Could not save server configuration")
		}
		return
	}
	if err = reservation.Commit(); err != nil {
		// Only undo our own section if nobody has changed it since this save.
		restore := old
		if !present {
			restore = nil
		}
		if rollback := configstore.CompareAndSwap(app.configDir, configstore.Server, configstore.Revision(saved, true), restore); rollback != nil {
			app.updates.notify("server-config")
			portConfigError(w, 500, "port_restart_save_incomplete", "Restart failed and the saved port changed concurrently; reload the saved configuration before retrying")
			return
		}
		portConfigError(w, 500, "port_restart_start_failed", "Could not arm the reserved successor; the old configuration was retained")
		return
	}
	accepted = true
	app.updates.notify("server-config")
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Connection", "close")
	w.WriteHeader(http.StatusAccepted)
	jsonOut(w, map[string]any{"accepted": true, "restarting": true, "address": "http://" + config.Address(), "instanceId": processInstanceID, "port": config.Port})
	if flush, ok := w.(http.Flusher); ok {
		flush.Flush()
	}
	// End this request before shutdown drains API requests. No listener is released
	// on the target port: it is already held by the independent successor.
	app.cancel()
}

// RunReserved is used by a prepared successor, not by ordinary startup.
func RunReserved(port int, listener net.Listener, onReady func(context.Context) func(), restart func(int) (func() error, error), reserve func(int) (*ReservedPortRestart, error)) error {
	if listener == nil || listener.Addr().String() != (RuntimeConfig{Port: port}).Address() {
		return errors.New("invalid reserved listener")
	}
	return Run(port, onReady, restart, RunOptions{Listener: listener, WaitForInstance: true, ReservePortRestart: reserve})
}

func waitInstanceLock(ctx context.Context, try func() error) error {
	timer := time.NewTimer(45 * time.Second)
	defer timer.Stop()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := try(); err == nil {
			return nil
		} else if !platform.IsLockBusy(err) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return errors.New("previous Vista instance did not release its lock")
		case <-ticker.C:
		}
	}
}
