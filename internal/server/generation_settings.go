package server

import (
	"context"
	"errors"
	"net/http"

	"vista/internal/generation"
	"vista/internal/model"
	"vista/internal/modelcatalog"
	"vista/internal/network"
	"vista/internal/provider"
)

func (a *App) editGeneration(ctx context.Context, edit modelcatalog.GenerationEdit, revision string) error {
	snapshot, err := a.modelSettingsSnapshot()
	if err != nil {
		return err
	}
	if revision == "" || revision != a.modelSettingsVersion(snapshot.revision) {
		return modelcatalog.NewSettingsError("settings_conflict", "Model settings changed. Reload them before saving.", nil)
	}
	if !snapshot.exists && edit.Connection != "" {
		return errors.New("save the service connection in Settings before assigning a specialized model")
	}
	raw, err := snapshot.raw.EditGeneration(edit)
	if err != nil {
		return err
	}
	a.modelMu.Lock()
	defer a.modelMu.Unlock()
	return a.commitModelSettingsLocked(ctx, snapshot, raw, false)
}

// Only candidate metadata for the requested adapter is loaded, on demand in
// the settings view. The shared models topic doesn't carry separate rosters.
func (a *App) generationCandidates(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method", 405)
		return
	}
	id, kind := r.URL.Query().Get("adapter"), r.URL.Query().Get("kind")
	if _, ok := generation.Find(id, kind); !ok {
		settingsHTTPError(w, errors.New("unknown generation adapter"), 400)
		return
	}
	id = generation.CatalogProviderID(id)
	w.Header().Set("Cache-Control", "no-store")
	jsonOut(w, provider.SpecializedModels(id, kind))
}

func (a *App) generationTargets(ctx context.Context) []model.GenerationTarget {
	// Media keeps using the saved account even if discovery replaces its chat anchor.
	a.modelMu.RLock()
	catalog := a.baseModels
	a.modelMu.RUnlock()
	ctx = network.WithConfigDir(ctx, a.configDir)
	var targets []model.GenerationTarget
	for _, row := range catalog.Models {
		if row.Generation == nil {
			continue
		}
		target := catalog.GenerationTarget(row)
		if metadata, found := provider.SpecializedModel(generation.CatalogProviderID(row.ProviderID), row.ID, target.Kind); found {
			target.InputModalities = metadata.Input
		}
		source := target.Connection
		if source != nil {
			_, bound, err := source.BindNetwork(ctx)
			if err != nil {
				target.Error = "The generation connection's network profile is unavailable."
			} else {
				*source = bound
			}
		}
		targets = append(targets, target)
	}
	return targets
}
