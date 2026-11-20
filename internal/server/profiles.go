package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	slashpath "path"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/google/uuid"
	"vista/internal/configstore"
	"vista/internal/instance"
	"vista/internal/platform"
)

const defaultProfile = "default"

type profile struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Archived bool   `json:"archived"`
}

type profileRuntime struct {
	*appHandler
	mutations sync.RWMutex
	unlock    func()
}

// No active profile on the server: the URL selects a runtime. The default
// runtime retains the original configuration, Store, routes and instance lock.
type profiles struct {
	op        sync.Mutex // Registry changes and opening/closing named runtimes.
	mu        sync.RWMutex
	items     []profile
	apps      map[string]*profileRuntime
	device    *App
	admission sync.Mutex
}

func openProfiles(app *App) (*profiles, error) {
	p := &profiles{device: app, apps: make(map[string]*profileRuntime), items: []profile{{ID: defaultProfile}}}
	data, exists, err := configstore.Read(app.configDir, configstore.Profiles)
	if err != nil {
		return nil, err
	}
	if !exists {
		return p, nil
	}
	if err = json.Unmarshal(data, &p.items); err != nil || len(p.items) == 0 || p.items[0].ID != defaultProfile || p.items[0].Archived {
		return nil, errors.New("invalid Profile registry")
	}
	seen := map[string]bool{}
	for _, entry := range p.items {
		if seen[entry.ID] || entry.ID != defaultProfile && (!validProfileID(entry.ID) || !validProfileName(entry.Name)) || entry.ID == defaultProfile && entry.Name != "" && !validProfileName(entry.Name) {
			return nil, errors.New("invalid Profile registry entry")
		}
		seen[entry.ID] = true
	}
	return p, nil
}

func validProfileName(name string) bool {
	return name != "" && len(name) <= 200 && !strings.ContainsAny(name, "\x00\n\r")
}
func validProfileID(id string) bool {
	parsed, err := uuid.Parse(id)
	return err == nil && parsed.String() == id
}

func (p *profiles) snapshot() []profile {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return slices.Clone(p.items)
}

func (p *profiles) scopedApps() []*App {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := []*App{p.device}
	for _, runtime := range p.apps {
		out = append(out, runtime.app)
	}
	return out
}

func (p *profiles) notify() {
	for _, a := range p.scopedApps() {
		a.updates.notify("profiles")
	}
}

func (p *profiles) configDir(id string) string {
	return filepath.Join(p.device.configDir, "profiles", id)
}

func (p *profiles) open(id string) (*profileRuntime, error) {
	// Caller holds op; capacity reads only the short-lived map lock.
	if runtime := p.apps[id]; runtime != nil {
		return runtime, nil
	}
	if err := p.device.ctx.Err(); err != nil {
		return nil, err
	}
	root := p.configDir(id)
	for _, dir := range []string{filepath.Dir(root), root} {
		info, err := os.Lstat(dir)
		if err != nil || !info.IsDir() || !platform.PrivateMode(info.Mode()) {
			return nil, errors.New("Profile directory is missing or is not private")
		}
	}
	state := filepath.Join(root, "state")
	if info, err := os.Lstat(state); err == nil && (!info.IsDir() || !platform.PrivateMode(info.Mode())) {
		return nil, errors.New("invalid Profile state directory")
	}
	lock, err := instance.Lock(state)
	if err != nil {
		return nil, err
	}
	release := true
	defer func() {
		if release {
			instance.Unlock(lock)
		}
	}()
	if err = configstore.Migrate(root); err != nil {
		return nil, err
	}
	app, err := loadApp(p.device.ctx, func() {}, root, state, p.device.buildInfo, p)
	if err != nil {
		return nil, err
	}
	app.listenerPort, app.listenerHost = p.device.listenerPort, p.device.listenerHost
	h := newAppHandler(app, nil)
	runtime := &profileRuntime{appHandler: h, unlock: func() { instance.Unlock(lock) }}
	p.mu.Lock()
	p.apps[id] = runtime
	p.mu.Unlock()
	release = false
	if err = app.start(); err != nil {
		p.closeRuntime(id)
		return nil, err
	}
	return runtime, nil
}

func (p *profiles) closeRuntime(id string) {
	runtime := p.apps[id]
	if runtime == nil {
		return
	}
	// Keep this instance in the capacity calculation until its workers drain.
	runtime.close()
	p.admission.Lock()
	p.mu.Lock()
	delete(p.apps, id)
	p.mu.Unlock()
	p.admission.Unlock()
	runtime.unlock()
}

func (p *profiles) close() {
	p.op.Lock()
	defer p.op.Unlock()
	for id := range p.apps {
		p.closeRuntime(id)
	}
}

func (p *profiles) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.URL.Path == "/api/profiles" || strings.HasPrefix(r.URL.Path, "/api/profiles/") {
		p.manage(w, r)
		return
	}
	id, path, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/profiles/"), "/")
	if path != "" && slashpath.Clean(path) != strings.TrimSuffix(path, "/") {
		http.NotFound(w, r)
		return
	}
	if !validProfileID(id) {
		http.Error(w, "Unknown Profile", 404)
		return
	}
	if strings.HasPrefix(path, "profiles/") || strings.HasPrefix(path, "api/profiles") {
		http.NotFound(w, r)
		return
	}
	if path == "api/reset" || path == "api/restart" || path == "api/stop" || path == "api/server-config" && r.Method != http.MethodGet {
		http.Error(w, "Use the default Profile for Vista-wide controls", 409)
		return
	}
	p.op.Lock()
	entry, ok := p.find(id)
	if !ok || entry.Archived {
		p.op.Unlock()
		if entry.Archived {
			http.Error(w, "This Profile is archived. Restore it from Profiles before opening it.", http.StatusGone)
		} else {
			http.Error(w, "Unknown Profile", 404)
		}
		return
	}
	// A document request needn't open the database. The SPA uses this URL as
	// its base and its authenticated data path opens the Profile on demand.
	if !strings.HasPrefix(path, "api/") {
		p.op.Unlock()
		next := r.Clone(r.Context())
		next.URL.Path = "/" + path
		next.URL.RawPath = ""
		p.device.root(w, next)
		return
	}
	runtime, err := p.open(id)
	if err != nil {
		p.op.Unlock()
		http.Error(w, "Could not open this Profile: "+err.Error(), 503)
		return
	}
	// Serialize potentially stateful requests with archive. Read requests and
	// SSE do not hold the registry lock and are drained by App teardown.
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		runtime.mutations.RLock()
		defer runtime.mutations.RUnlock()
	}
	p.op.Unlock()
	next := r.Clone(r.Context())
	next.URL.Path = "/" + path
	next.URL.RawPath = ""
	runtime.ServeHTTP(w, next)
}

func (p *profiles) find(id string) (profile, bool) {
	for _, entry := range p.items {
		if entry.ID == id {
			return entry, true
		}
	}
	return profile{}, false
}

func (p *profiles) manage(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/api/profiles" && r.Method == http.MethodGet {
		jsonOut(w, p.snapshot())
		return
	}
	if r.Method != http.MethodPost && r.Method != http.MethodPatch {
		http.Error(w, "method", 405)
		return
	}
	var input struct {
		Name     *string `json:"name"`
		Archived *bool   `json:"archived"`
	}
	if decodeSettingsRequest(http.MaxBytesReader(w, r.Body, 4096), &input) != nil || (input.Name == nil) == (input.Archived == nil) {
		http.Error(w, "Provide a name or archive state", 400)
		return
	}
	if input.Name != nil {
		v := strings.TrimSpace(*input.Name)
		if !validProfileName(v) {
			http.Error(w, "Enter a name of at most 200 bytes", 400)
			return
		}
		input.Name = &v
	}
	p.op.Lock()
	defer p.op.Unlock()
	if p.device.ctx.Err() != nil {
		http.Error(w, "Vista is stopping", 503)
		return
	}
	items := p.snapshot()
	var entry profile
	created := false
	var createdPath string
	if r.URL.Path == "/api/profiles" && r.Method == http.MethodPost && input.Name != nil {
		entry = profile{ID: uuid.NewString(), Name: *input.Name}
		createdPath = p.configDir(entry.ID)
		if err := p.createDirectory(createdPath); err != nil {
			http.Error(w, "Could not create Profile: "+err.Error(), 500)
			return
		}
		items = append(items, entry)
		created = true
	} else if r.Method == http.MethodPatch {
		id := strings.TrimPrefix(r.URL.Path, "/api/profiles/")
		index := slices.IndexFunc(items, func(e profile) bool { return e.ID == id })
		if index < 0 {
			http.NotFound(w, r)
			return
		}
		entry = items[index]
		if input.Name != nil {
			entry.Name = *input.Name
		} else {
			if entry.ID == defaultProfile && *input.Archived {
				http.Error(w, "The default Profile cannot be archived", 409)
				return
			}
			if rt := p.apps[id]; rt != nil && *input.Archived {
				rt.mutations.Lock()
				defer rt.mutations.Unlock()
				p.admission.Lock()
				counts, err := rt.app.store.SessionCounts(r.Context())
				if err != nil || counts.Running > 0 {
					p.admission.Unlock()
					http.Error(w, "This Profile cannot be archived while work is running", 409)
					return
				}
				// Cancel the now-idle dispatcher before freeing admission; no queued
				// run can cross the archive decision.
				rt.app.closeContext()
				p.admission.Unlock()
				p.closeRuntime(id)
			}
			entry.Archived = *input.Archived
		}
		items[index] = entry
	} else {
		http.Error(w, "invalid Profile action", 400)
		return
	}
	data, err := json.Marshal(items)
	if err == nil {
		err = configstore.Write(p.device.configDir, configstore.Profiles, data)
	}
	if err != nil {
		if created {
			_ = os.RemoveAll(createdPath)
		} // Only the fresh, unregistered directory.
		http.Error(w, "Could not save the Profile registry", 500)
		return
	}
	p.mu.Lock()
	p.items = items
	p.mu.Unlock()
	p.notify()
	if created {
		w.WriteHeader(http.StatusCreated)
	}
	jsonOut(w, entry)
}

func (p *profiles) createDirectory(path string) (err error) {
	dir := filepath.Dir(path)
	if err = os.Mkdir(dir, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || !platform.PrivateMode(info.Mode()) {
		return errors.New("invalid Profiles directory")
	}
	if err = os.Mkdir(path, 0700); err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = os.RemoveAll(path)
		}
	}()
	// An explicitly empty catalog avoids inheriting environment credentials.
	return configstore.Write(path, configstore.Models, []byte(`{"default":"","models":[]}`))
}

func (a *App) deviceConfigDir() string {
	if a.profiles != nil {
		return a.profiles.device.configDir
	}
	return a.configDir
}

func (a *App) admissionLock() *sync.Mutex {
	if a.profiles != nil {
		return &a.profiles.admission
	}
	return &a.admissionMu
}

func (a *App) activeExecutions(ctx context.Context, own int) (int, error) {
	if a.profiles == nil {
		return own, nil
	}
	for _, sibling := range a.profiles.scopedApps() {
		if sibling == a {
			continue
		}
		counts, err := sibling.store.SessionCounts(ctx)
		if err != nil {
			return 0, fmt.Errorf("check Profile capacity: %w", err)
		}
		own += counts.Running
	}
	return own, nil
}
