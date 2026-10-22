package server

import (
	"context"
	"errors"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"vista/internal/codexauth"
	"vista/internal/modelcatalog"
)

// A settings edit reads a detached snapshot, performs network work without the
// directory lock, then commits only if the configuration has not changed.
type modelSettingsSnapshot struct {
	raw, live modelCatalog
	exists    bool
	revision  uint64
}

func cloneCatalog(catalog modelCatalog) modelCatalog {
	catalog.Models = append([]configuredModel(nil), catalog.Models...)
	for i := range catalog.Models {
		catalog.Models[i].Headers = maps.Clone(catalog.Models[i].Headers)
	}
	return catalog
}

func (a *App) modelSettingsSnapshot() (modelSettingsSnapshot, error) {
	a.modelMu.RLock()
	defer a.modelMu.RUnlock()
	raw, exists, err := modelcatalog.Read(a.configDir)
	if err != nil {
		return modelSettingsSnapshot{}, modelcatalog.NewSettingsError("settings_unavailable", "Could not read model settings.", nil)
	}
	if !exists {
		raw = cloneCatalog(a.baseModels)
	}
	return modelSettingsSnapshot{raw: raw, live: cloneCatalog(a.models), exists: exists, revision: a.modelRevision}, nil
}

func (a *App) modelCredentialEnvironment() []string {
	a.modelMu.RLock()
	defer a.modelMu.RUnlock()
	return a.baseModels.CredentialEnvironment()
}

func (a *App) commitModelSettings(ctx context.Context, snapshot modelSettingsSnapshot, raw modelCatalog) error {
	a.modelMu.Lock()
	defer a.modelMu.Unlock()
	return a.commitModelSettingsLocked(ctx, snapshot, raw)
}

func (a *App) commitModelSettingsLocked(ctx context.Context, snapshot modelSettingsSnapshot, raw modelCatalog) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if a.modelRevision != snapshot.revision {
		return modelcatalog.NewSettingsError("settings_conflict", "Model settings changed while this request was in progress. Retry the change.", nil)
	}
	if len(raw.Models) == 0 {
		// Resolve the fallback before replacing the current configuration.
		fallback, err := modelcatalog.FromEnvironment()
		if err != nil {
			return err
		}
		if err := os.Remove(filepath.Join(a.configDir, "models.json")); err != nil && !errors.Is(err, os.ErrNotExist) {
			return modelcatalog.NewSettingsError("settings_unavailable", "Could not remove model settings.", nil)
		}
		a.publishBaseModelsLocked(fallback)
		return a.removeDisconnectedCodex(snapshot.raw, raw)
	}
	resolved, err := modelcatalog.Save(a.configDir, raw)
	if err != nil {
		return err
	}
	a.publishBaseModelsLocked(resolved)
	if a.ctx != nil {
		a.background.Go(func() { a.refreshCustomModels(a.ctx) })
		a.background.Go(func() { a.refreshCopilotModels(a.ctx) })
	}
	return a.removeDisconnectedCodex(snapshot.raw, raw)
}

func (a *App) removeDisconnectedCodex(before, after modelCatalog) error {
	retained := map[string]bool{}
	for _, model := range after.Models {
		retained[model.ConnectionID] = true
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, model := range before.Models {
		if model.ProviderID != "openai-codex" || retained[model.ConnectionID] {
			continue
		}
		if err := codexauth.Remove(ctx, codexauth.Path(a.configDir, model.ConnectionID)); err != nil {
			return errors.New("provider disconnected, but its local Codex credential could not be removed")
		}
		retained[model.ConnectionID] = true
	}
	return nil
}

func (a *App) publishBaseModelsLocked(resolved modelCatalog) {
	a.baseModels = resolved
	a.modelRevision++
	a.modelDiscoveries = nil
	active := make(map[string]bool)
	for _, source := range customSources(resolved) {
		active[source.identity()] = true
	}
	for identity := range a.customInventories {
		if !active[identity] {
			delete(a.customInventories, identity)
		}
	}
	active = make(map[string]bool)
	for _, source := range copilotSources(resolved) {
		active[modelcatalog.CopilotSourceID(source)] = true
	}
	for identity := range a.copilotInventories {
		if !active[identity] {
			delete(a.copilotInventories, identity)
		}
	}
	a.rebuildModelsLocked()
}

func (a *App) changeProviderSettings(ctx context.Context, method string, input providerSettingsInput) error {
	snapshot, err := a.modelSettingsSnapshot()
	if err != nil {
		return err
	}
	raw, err := modelcatalog.ChangeConnection(ctx, snapshot.raw, snapshot.exists, method, input)
	if err != nil {
		return err
	}
	return a.commitModelSettings(ctx, snapshot, raw)
}

func (a *App) selectDefaultModel(ctx context.Context, key string) error {
	snapshot, err := a.modelSettingsSnapshot()
	if err != nil {
		return err
	}
	raw, err := modelcatalog.SelectDefault(snapshot.raw, snapshot.live, key)
	if err != nil {
		return err
	}
	return a.commitModelSettings(ctx, snapshot, raw)
}

func settingsHTTPError(w http.ResponseWriter, err error, status int) {
	var detail *modelcatalog.SettingsError
	if !errors.As(err, &detail) {
		code := "invalid_settings"
		if status >= 500 {
			code = "settings_unavailable"
		}
		if status == http.StatusMethodNotAllowed {
			code = "method_not_allowed"
		}
		detail = modelcatalog.NewSettingsError(code, err.Error(), nil)
	}
	switch detail.Code {
	case "settings_conflict":
		status = http.StatusConflict
	case "settings_unavailable":
		if status < 500 {
			status = http.StatusInternalServerError
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	jsonOut(w, detail)
}

// Each source has a newest request ticket. A slow earlier response may not
// replace a newer inventory, even if the source configuration is unchanged.
func (a *App) beginModelDiscoveryLocked(source string) uint64 {
	if a.modelDiscoveries == nil {
		a.modelDiscoveries = make(map[string]uint64)
	}
	a.modelDiscoverySequence++
	a.modelDiscoveries[source] = a.modelDiscoverySequence
	return a.modelDiscoverySequence
}
func (a *App) currentModelDiscoveryLocked(source string, ticket uint64) bool {
	return a.modelDiscoveries[source] == ticket
}
