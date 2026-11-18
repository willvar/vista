package server

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/willvar/dofs"
	"vista/internal/configstore"
	"vista/internal/resources"
)

type resourceStorageConfigView struct {
	ObjectStorage *resources.PublicConfig `json:"objectStorage,omitempty"`
	Current       resources.PublicConfig  `json:"current"`
	Revision      string                  `json:"revision"`
	Migration     *resources.Migration    `json:"migration,omitempty"`
	Target        *resources.PublicConfig `json:"target,omitempty"`
}
type resourceStorageInput struct {
	Kind         string `json:"kind"`
	Endpoint     string `json:"endpoint,omitempty"`
	Bucket       string `json:"bucket,omitempty"`
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
	MigrationID string                `json:"migrationId,omitempty"`
}

func (a *App) storageConfiguration() (resourceStorageConfigView, error) {
	control := a.store.ResourceControl()
	stamp, err := control.Current(a.ctxOrBackground())
	if err != nil {
		return resourceStorageConfigView{}, err
	}
	settings, err := resources.LoadSettings(control.Library.ConfigDir())
	if err != nil {
		return resourceStorageConfigView{}, err
	}
	backend, err := settings.Backend(stamp.BackendID)
	if err != nil {
		return resourceStorageConfigView{}, err
	}
	raw, present, err := configstore.Read(control.Library.ConfigDir(), configstore.Storage)
	if err != nil {
		return resourceStorageConfigView{}, err
	}
	material, _ := json.Marshal(struct {
		Stamp  resources.Stamp
		Config json.RawMessage
	}{stamp, raw})
	view := resourceStorageConfigView{Current: backend.Public(), Revision: a.networkRevision(material, present)}
	for _, saved := range settings.Backends {
		if saved.Kind == "s3" {
			value := saved.Public()
			view.ObjectStorage = &value
		}
	}
	if backend.Kind == "s3" {
		value := backend.Public()
		view.ObjectStorage = &value
	}
	view.Migration, err = control.Migration(a.ctxOrBackground())
	if err != nil {
		return view, err
	}
	if backend.Kind == "local" && view.Migration != nil && view.Migration.Phase == "complete" {
		if previous, e := settings.Backend(view.Migration.Source); e == nil && previous.Kind == "s3" {
			value := previous.Public()
			view.ObjectStorage = &value
		}
	}
	if view.Migration != nil && view.Migration.Protected() {
		target, e := settings.Backend(view.Migration.Target)
		if e != nil {
			return view, e
		}
		value := target.Public()
		view.Target = &value
	}
	return view, nil
}
func (a *App) ctxOrBackground() context.Context {
	if a.ctx != nil {
		return a.ctx
	}
	return context.Background()
}

func (a *App) configureResourceStorage(w http.ResponseWriter, r *http.Request, input resourceStorageRequest) {
	if input.Config == nil || input.Revision == "" {
		settingsHTTPError(w, errors.New("storage configuration and revision are required"), 400)
		return
	}
	a.storageConfigMu.Lock()
	defer a.storageConfigMu.Unlock()
	control := a.store.ResourceControl()
	view, err := a.storageConfiguration()
	if err != nil {
		settingsHTTPError(w, errors.New("storage configuration could not be read"), 500)
		return
	}
	if view.Revision != input.Revision {
		settingsHTTPError(w, configstore.ErrConflict, 409)
		return
	}
	if view.Migration != nil && view.Migration.Protected() {
		settingsHTTPError(w, resources.ErrMigrationBusy, 409)
		return
	}
	expected, err := control.Current(r.Context())
	if err != nil {
		settingsHTTPError(w, err, 500)
		return
	}
	cfg := resources.BackendConfig{ID: resources.LocalBackend, Kind: "local"}
	if input.Config.Kind != "local" && input.Config.Kind != "s3" {
		settingsHTTPError(w, errors.New("invalid storage kind"), 400)
		return
	}
	if input.Config.Kind == "s3" {
		cfg = resources.BackendConfig{Kind: "s3", Endpoint: input.Config.Endpoint, Bucket: input.Config.Bucket, Region: input.Config.Region, Prefix: input.Config.Prefix, AccessKey: input.Config.AccessKey, SecretKey: input.Config.SecretKey, SessionToken: input.Config.SessionToken}
		err = configstore.Update(control.Library.ConfigDir(), configstore.Storage, func(data []byte, present bool) ([]byte, error) {
			material, _ := json.Marshal(struct {
				Stamp  resources.Stamp
				Config json.RawMessage
			}{expected, data})
			if a.networkRevision(material, present) != input.Revision {
				return nil, configstore.ErrConflict
			}
			var settings resources.Settings
			if present && json.Unmarshal(data, &settings) != nil {
				return nil, errors.New("invalid storage configuration")
			}
			found := -1
			for i, old := range settings.Backends {
				if old.ID != expected.BackendID && cfg.SameLocation(old) {
					found = i
					cfg.ID, cfg.MasterKey = old.ID, old.MasterKey
					if cfg.AccessKey == "" && cfg.SecretKey == "" {
						cfg.AccessKey, cfg.SecretKey = old.AccessKey, old.SecretKey
						if cfg.SessionToken == "" {
							cfg.SessionToken = old.SessionToken
						}
					}
					break
				}
			}
			if cfg.ID == "" && cfg.AccessKey == "" && cfg.SecretKey == "" {
				for _, active := range settings.Backends {
					if active.ID == expected.BackendID && cfg.SameLocation(active) {
						cfg.AccessKey, cfg.SecretKey = active.AccessKey, active.SecretKey
						if cfg.SessionToken == "" {
							cfg.SessionToken = active.SessionToken
						}
						break
					}
				}
			}
			if cfg.ID == "" {
				cfg.ID = uuid.NewString()
				key, e := dofs.GenerateKey()
				if e != nil {
					return nil, e
				}
				cfg.MasterKey = hex.EncodeToString(key)
				dofs.Clear(key)
			}
			if err := cfg.Validate(); err != nil {
				return nil, err
			}
			if found < 0 {
				settings.Backends = append(settings.Backends, cfg)
			} else {
				settings.Backends[found] = cfg
			}
			return json.Marshal(settings)
		})
		if err != nil {
			code := 400
			if errors.Is(err, configstore.ErrConflict) {
				code = 409
			}
			settingsHTTPError(w, err, code)
			return
		}
		// Persisted namespace credentials/identity precede any test or upload. Even
		// interrupted verification objects can then be found by ordinary cleanup.
		ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
		defer cancel()
		if err = resources.Prepare(ctx, control.Library.StateDir, cfg); err == nil {
			err = resources.CheckBackend(ctx, control.Library.StateDir, cfg)
		}
		if err != nil {
			a.invalidateResourceStorage()
			a.updates.notify("storage")
			result := a.resourceStorageSnapshot()
			result.CheckError = "Object storage read/write/delete verification failed. Check the endpoint, bucket, permissions and versioning."
			jsonOut(w, result)
			return
		}
	}
	if input.Action == "migrate" {
		settings, loadErr := resources.LoadSettings(control.Library.ConfigDir())
		if loadErr != nil {
			settingsHTTPError(w, loadErr, 500)
			return
		}
		active, loadErr := settings.Backend(expected.BackendID)
		if loadErr != nil {
			settingsHTTPError(w, loadErr, 500)
			return
		}
		if cfg.SameLocation(active) {
			release, lockErr := control.Library.Lock(r.Context())
			if lockErr != nil {
				settingsHTTPError(w, lockErr, 409)
				return
			}
			current, stateErr := control.Current(r.Context())
			if stateErr == nil && current != expected {
				stateErr = resources.ErrStorageChanged
			}
			if stateErr == nil && cfg.Kind == "s3" {
				stateErr = configstore.Update(control.Library.ConfigDir(), configstore.Storage, func(raw []byte, present bool) ([]byte, error) {
					var latest resources.Settings
					if json.Unmarshal(raw, &latest) != nil {
						return nil, errors.New("invalid storage configuration")
					}
					for i, backend := range latest.Backends {
						if backend.ID == active.ID {
							backend.AccessKey, backend.SecretKey, backend.SessionToken, backend.Region = cfg.AccessKey, cfg.SecretKey, cfg.SessionToken, cfg.Region
							latest.Backends[i] = backend
						}
					}
					return json.Marshal(latest)
				})
			}
			release()
			if stateErr != nil {
				settingsHTTPError(w, stateErr, 409)
				return
			}
			a.updates.notify("storage")
			jsonOut(w, a.resourceStorageSnapshot())
			return
		}
		if _, err = control.Start(r.Context(), cfg.ID, expected); err != nil {
			settingsHTTPError(w, err, 409)
			return
		}
		a.wakeResourceMigration()
	}
	a.invalidateResourceStorage()
	a.updates.notify("storage")
	jsonOut(w, a.resourceStorageSnapshot())
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
