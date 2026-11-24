package server

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/willvar/dofs"
	"vista/internal/configstore"
	"vista/internal/resources"
)

type resourceStoragePresetView struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	resources.PublicConfig
}

type resourceStorageConfigView struct {
	Current        resources.PublicConfig      `json:"current"`
	ActivePresetID string                      `json:"activePresetId"`
	Presets        []resourceStoragePresetView `json:"presets"`
	Revision       string                      `json:"revision"`
	Migration      *resources.Migration        `json:"migration,omitempty"`
	TargetPresetID *string                     `json:"targetPresetId,omitempty"`
}

type resourceStorageInput struct {
	Name         string `json:"name"`
	Endpoint     string `json:"endpoint"`
	Bucket       string `json:"bucket"`
	Region       string `json:"region,omitempty"`
	Prefix       string `json:"prefix,omitempty"`
	AccessKey    string `json:"accessKey,omitempty"`
	SecretKey    string `json:"secretKey,omitempty"`
	SessionToken string `json:"sessionToken,omitempty"`
}

type resourceStorageRequest struct {
	Action      string                `json:"action"`
	Config      *resourceStorageInput `json:"config,omitempty"`
	Revision    string                `json:"revision,omitempty"`
	PresetID    *string               `json:"presetId,omitempty"`
	MigrationID string                `json:"migrationId,omitempty"`
}

type resourceStorageState struct {
	current        resources.Stamp
	settings       resources.Settings
	migration      *resources.Migration
	storedRevision string
}

func (a *App) storageRevision(stamp resources.Stamp, data []byte, present bool) string {
	material, _ := json.Marshal(struct {
		Stamp  resources.Stamp
		Config json.RawMessage
	}{stamp, data})
	return a.networkRevision(material, present)
}

func (a *App) storageEditorRevision(s resourceStorageState) string {
	// Draft probes and removed backends are kept for recovery, not editable
	// choices. Keep them out of the editor revision, but not the disk guard.
	selected := map[string]bool{s.current.BackendID: true}
	for _, preset := range s.settings.Presets {
		selected[preset.BackendID] = true
	}
	backends := make([]resources.BackendConfig, 0, len(selected))
	for _, backend := range s.settings.Backends {
		if selected[backend.ID] {
			backends = append(backends, backend)
		}
	}
	data, _ := json.Marshal(resources.Settings{Backends: backends, Presets: s.settings.Presets})
	return a.storageRevision(s.current, data, true)
}
func (a *App) readStorageState(ctx context.Context) (state resourceStorageState, err error) {
	control := a.store.ResourceControl()
	if state.current, err = control.Current(ctx); err != nil {
		return state, err
	}
	data, present, err := configstore.Read(control.Library.ConfigDir(), configstore.Storage)
	if err != nil {
		return state, err
	}
	if state.settings, err = resources.DecodeSettings(data); err != nil {
		return state, err
	}
	if state.migration, err = control.Migration(ctx); err != nil {
		return state, err
	}
	state.storedRevision = a.storageRevision(state.current, data, present)
	state.projectLegacyPresets()
	return state, nil
}

// Preserve the previous chooser: the active or last selected remote location,
// and any in-progress target. Shadow test namespaces are not user presets.
// IDs/names are deterministic, and reads do not rewrite existing settings.
func (s *resourceStorageState) projectLegacyPresets() {
	if s.settings.Presets != nil {
		return
	}
	s.settings.Presets = []resources.Preset{}
	add := func(id string) {
		backend, err := s.settings.Backend(id)
		if err != nil || backend.Kind != "s3" || backend.Unused || id == resources.LocalBackend {
			return
		}
		name := "DOFS"
		if len(s.settings.Presets) > 0 {
			name = "DOFS 2"
		}
		s.settings.Presets = append(s.settings.Presets, resources.Preset{ID: id, Name: name, BackendID: id})
	}
	add(s.current.BackendID)
	if job := s.migration; job != nil {
		if job.Protected() && job.Target != s.current.BackendID {
			add(job.Target)
		} else if job.Phase == "complete" && len(s.settings.Presets) == 0 {
			add(job.Source)
		}
	}
	if len(s.settings.Presets) == 0 {
		for i := len(s.settings.Backends) - 1; i >= 0 && len(s.settings.Presets) == 0; i-- {
			add(s.settings.Backends[i].ID)
		}
	}
}

func (a *App) storageConfiguration() (resourceStorageConfigView, error) {
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	s, err := a.readStorageState(ctx)
	if err != nil {
		return resourceStorageConfigView{}, err
	}
	active, err := s.settings.Backend(s.current.BackendID)
	if err != nil {
		return resourceStorageConfigView{}, err
	}
	view := resourceStorageConfigView{Current: active.Public(), Revision: a.storageEditorRevision(s), Migration: s.migration, Presets: make([]resourceStoragePresetView, 0, len(s.settings.Presets))}
	for _, preset := range s.settings.Presets {
		backend, err := s.settings.Backend(preset.BackendID)
		if err != nil {
			return view, err
		}
		view.Presets = append(view.Presets, resourceStoragePresetView{ID: preset.ID, Name: preset.Name, PublicConfig: backend.Public()})
		if preset.BackendID == s.current.BackendID {
			view.ActivePresetID = preset.ID
		}
		if job := s.migration; job != nil && job.Protected() && job.Target == preset.BackendID {
			view.TargetPresetID = &preset.ID
		}
	}
	if job := s.migration; job != nil && job.Protected() && job.Target == resources.LocalBackend {
		view.TargetPresetID = new(string)
	}
	return view, nil
}

// No remote requests under the publication lock. The stamp closes the race
// between reading the active backend and editing/removing its configuration.
func (a *App) writeStorageState(ctx context.Context, s *resourceStorageState) error {
	control := a.store.ResourceControl()
	release, err := control.Library.Lock(ctx)
	if err != nil {
		return err
	}
	defer release()
	if stamp, err := control.Current(ctx); err != nil {
		return err
	} else if stamp != s.current {
		return configstore.ErrConflict
	}
	dir := control.Library.ConfigDir()
	err = configstore.Update(dir, configstore.Storage, func(raw []byte, present bool) ([]byte, error) {
		if a.storageRevision(s.current, raw, present) != s.storedRevision {
			return nil, configstore.ErrConflict
		}
		return json.Marshal(s.settings)
	})
	if err != nil {
		return err
	}
	raw, present, err := configstore.Read(dir, configstore.Storage)
	if err == nil {
		s.storedRevision = a.storageRevision(s.current, raw, present)
	}
	return err
}

func newStorageBackend(cfg resources.BackendConfig) (resources.BackendConfig, error) {
	cfg.ID, cfg.Unused = uuid.NewString(), true
	key, err := dofs.GenerateKey()
	if err != nil {
		return cfg, err
	}
	cfg.MasterKey = hex.EncodeToString(key)
	dofs.Clear(key)
	return cfg, nil
}

var errStorageVerification = errors.New("Object storage read/write/delete verification failed. Check the endpoint, bucket, permissions and versioning.")

func (a *App) verifyStorage(ctx context.Context, cfg resources.BackendConfig) error {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	dir := a.store.ResourceControl().Library.StateDir
	if resources.Prepare(ctx, dir, cfg) != nil || resources.CheckBackend(ctx, dir, cfg) != nil {
		return errStorageVerification
	}
	return nil
}

// A probe with unsaved connection settings gets an isolated namespace. Persist
// its recovery credentials before writing any object; it is never selectable.
func (a *App) probeStorageDraft(ctx context.Context, state *resourceStorageState, cfg resources.BackendConfig) error {
	probe, err := newStorageBackend(cfg)
	if err != nil {
		return err
	}
	state.settings.Backends = append(state.settings.Backends, probe)
	if err := a.writeStorageState(ctx, state); err != nil {
		return err
	}
	testErr := a.verifyStorage(ctx, probe)
	library := a.store.ResourceControl().Library
	if needed, err := library.NeedsInventory(probe); err == nil && !needed && ctx.Err() == nil {
		state.settings.Backends = state.settings.Backends[:len(state.settings.Backends)-1]
		if err := a.writeStorageState(ctx, state); err != nil {
			return err
		}
		// The UUID belongs only to this probe and no durable intent remains.
		_ = os.RemoveAll(filepath.Join(library.StateDir, "resource-backends", probe.ID))
	}
	return testErr
}

func (a *App) configureResourceStorage(w http.ResponseWriter, r *http.Request, input resourceStorageRequest) {
	formAction := input.Action == "check" || input.Action == "save"
	if input.Revision == "" || formAction != (input.Config != nil) || !formAction && input.PresetID == nil || input.MigrationID != "" {
		settingsHTTPError(w, errors.New("invalid storage configuration request"), 400)
		return
	}
	presetID := ""
	if input.PresetID != nil {
		presetID = *input.PresetID
	}
	a.storageConfigMu.Lock()
	defer a.storageConfigMu.Unlock()
	ctx := r.Context()
	s, err := a.readStorageState(ctx)
	if err != nil {
		settingsHTTPError(w, errors.New("storage configuration could not be read"), 500)
		return
	}
	if a.storageEditorRevision(s) != input.Revision {
		settingsHTTPError(w, configstore.ErrConflict, 409)
		return
	}
	if s.migration != nil && s.migration.Protected() {
		settingsHTTPError(w, resources.ErrMigrationBusy, 409)
		return
	}
	index := slices.IndexFunc(s.settings.Presets, func(p resources.Preset) bool { return p.ID == presetID })
	if index < 0 && !(presetID == "" && (formAction || input.Action == "migrate")) {
		settingsHTTPError(w, errors.New("storage configuration not found"), 404)
		return
	}
	var preset resources.Preset
	var old resources.BackendConfig
	if index >= 0 {
		preset = s.settings.Presets[index]
		old, _ = s.settings.Backend(preset.BackendID) // validated by DecodeSettings
	}
	control := a.store.ResourceControl()
	switch input.Action {
	case "remove":
		if old.ID == s.current.BackendID {
			settingsHTTPError(w, errors.New("switch to another storage location before removing this configuration"), 409)
			return
		}
		s.settings.Presets = slices.Delete(s.settings.Presets, index, index+1)
		// Keep old physical backends for resource accounting and cleanup.
		err = a.writeStorageState(ctx, &s)
	case "migrate":
		target := old.ID
		if presetID == "" {
			target = resources.LocalBackend
		}
		if target == s.current.BackendID {
			break
		}
		if target != resources.LocalBackend {
			err = a.verifyStorage(ctx, old)
			a.invalidateResourceStorage()
			if err != nil {
				break
			}
			if old.Unused {
				for i := range s.settings.Backends {
					if s.settings.Backends[i].ID == target {
						s.settings.Backends[i].Unused = false
					}
				}
				if err = a.writeStorageState(ctx, &s); err != nil {
					break
				}
			}
		}
		_, err = control.Start(ctx, target, s.current)
		if err == nil {
			a.wakeResourceMigration()
			a.invalidateResourceStorage()
		}
	case "check", "save":
		var status int
		status, err = a.editStoragePreset(ctx, &s, index, preset, old, *input.Config, input.Action == "check")
		if status != 0 {
			settingsHTTPError(w, err, status)
			return
		}
	}
	if errors.Is(err, errStorageVerification) {
		result := a.resourceStorageSnapshot()
		result.CheckError = errStorageVerification.Error()
		jsonOut(w, result)
		return
	}
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, configstore.ErrConflict) || errors.Is(err, resources.ErrStorageChanged) || errors.Is(err, resources.ErrMigrationBusy) {
			status = http.StatusConflict
		}
		settingsHTTPError(w, err, status)
		return
	}
	jsonOut(w, a.resourceStorageSnapshot())
}

func (a *App) editStoragePreset(ctx context.Context, state *resourceStorageState, index int, preset resources.Preset, old resources.BackendConfig, input resourceStorageInput, check bool) (int, error) {
	name := strings.TrimSpace(input.Name)
	if !resources.ValidPresetName(name) {
		return 400, errors.New("a storage configuration name is required (up to 128 characters)")
	}
	cfg := resources.BackendConfig{Kind: "s3", Endpoint: strings.TrimSpace(input.Endpoint), Bucket: strings.TrimSpace(input.Bucket), Region: strings.TrimSpace(input.Region), Prefix: strings.TrimSpace(input.Prefix), AccessKey: input.AccessKey, SecretKey: input.SecretKey, SessionToken: input.SessionToken}
	sameLocation := old.ID != "" && cfg.SameLocation(old)
	if old.ID == state.current.BackendID && !sameLocation {
		return 409, errors.New("add another configuration and switch to it to change the active location")
	}
	if sameLocation {
		cfg.ID, cfg.MasterKey, cfg.Unused = old.ID, old.MasterKey, old.Unused
		if cfg.AccessKey == "" && cfg.SecretKey == "" {
			cfg.AccessKey, cfg.SecretKey = old.AccessKey, old.SecretKey
			if cfg.SessionToken == "" {
				cfg.SessionToken = old.SessionToken
			}
		}
	} else {
		var err error
		if cfg, err = newStorageBackend(cfg); err != nil {
			return 0, err
		}
	}
	if err := cfg.Validate(); err != nil {
		return 400, err
	}
	if check || sameLocation && cfg != old {
		var err error
		if cfg == old {
			err = a.verifyStorage(ctx, cfg)
		} else {
			err = a.probeStorageDraft(ctx, state, cfg)
		}
		a.invalidateResourceStorage()
		if check || err != nil {
			return 0, err
		}
	}
	if index < 0 && len(state.settings.Presets) >= resources.MaxPresets {
		return 400, errors.New("too many storage configurations")
	}
	for i, p := range state.settings.Presets {
		if i == index {
			continue
		}
		if strings.EqualFold(p.Name, name) {
			return 409, errors.New("a storage configuration already has this name")
		}
		other, _ := state.settings.Backend(p.BackendID)
		if cfg.SameLocation(other) {
			return 409, errors.New("this storage location already has a configuration")
		}
	}
	if sameLocation {
		for i := range state.settings.Backends {
			if state.settings.Backends[i].ID == old.ID {
				state.settings.Backends[i] = cfg
			}
		}
	} else {
		state.settings.Backends = append(state.settings.Backends, cfg)
	}
	if index < 0 {
		preset.ID = uuid.NewString()
	}
	preset.Name, preset.BackendID = name, cfg.ID
	if index < 0 {
		state.settings.Presets = append(state.settings.Presets, preset)
	} else {
		state.settings.Presets[index] = preset
	}
	return 0, a.writeStorageState(ctx, state)
}

func (a *App) wakeResourceMigration() {
	if a.migrationWake == nil {
		return
	}
	select {
	case a.migrationWake <- struct{}{}:
	default:
	}
}
func (a *App) runResourceMigrations() {
	for {
		select {
		case <-a.ctx.Done():
			return
		case <-a.migrationWake:
		}
		a.runResourceMigration()
	}
}
func (a *App) runResourceMigration() {
	control := a.store.ResourceControl()
	job, err := control.Migration(a.ctx)
	if err != nil || job == nil || !job.Protected() {
		return
	}
	if job.Phase == "cancelling" {
		_ = control.FinishCancellation(a.ctx, job.ID)
		a.invalidateResourceStorage()
		a.updates.notify("storage")
		return
	}
	if job.Phase != "syncing" {
		return
	}
	ctx, cancel := context.WithCancel(a.ctx)
	a.storageMigrationMu.Lock()
	a.storageMigrationID = job.ID
	a.storageMigrationCancel = cancel
	a.storageMigrationMu.Unlock()
	defer func() {
		cancel()
		a.storageMigrationMu.Lock()
		a.storageMigrationID = ""
		a.storageMigrationCancel = nil
		a.storageMigrationMu.Unlock()
		a.invalidateResourceStorage()
		a.updates.notify("storage")
	}()
	// Reconstruct proof after process restart; never trust a marker alone.
	if err = control.Reverify(ctx, *job); err == nil {
		for ctx.Err() == nil {
			var files []resources.File
			files, err = control.Missing(ctx, *job)
			if err != nil {
				break
			}
			for _, file := range files {
				if err = control.Copy(ctx, *job, file); err != nil {
					break
				}
				a.updates.notify("storage")
			}
			if err != nil {
				break
			}
			var done bool
			done, err = control.Cutover(ctx, *job)
			if err != nil || done {
				break
			}
			// Missing is authoritative even if the publication wake was coalesced.
		}
	}
	if a.ctx.Err() != nil {
		return
	} // Shutdown retains resumable syncing state.
	latest, readErr := control.Migration(a.ctx)
	if readErr == nil && latest != nil && latest.ID == job.ID && latest.Phase == "cancelling" {
		_ = control.FinishCancellation(a.ctx, job.ID)
		return
	}
	if err != nil && !errors.Is(err, context.Canceled) {
		_ = control.Fail(a.ctx, job.ID)
	}
}
func (a *App) cancelResourceMigration(ctx context.Context, id string) error {
	if err := a.store.ResourceControl().Cancel(ctx, id); err != nil {
		return err
	}
	a.storageMigrationMu.Lock()
	if a.storageMigrationID == id && a.storageMigrationCancel != nil {
		a.storageMigrationCancel()
	}
	a.storageMigrationMu.Unlock()
	a.wakeResourceMigration()
	return nil
}
