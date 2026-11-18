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
	if edit.ProxyID != "" {
		settings, err := network.Load(a.configDir)
		if err != nil {
			return err
		}
		if _, err = settings.Select(edit.ProxyID); err != nil {
			return err
		}
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
func upstreamGenerationCatalogAdapter(id string) string {
	switch id {
	case "openai-codex":
		return "openai"
	case "together":
		return "togetherai"
	case "minimax-portal":
		return "minimax"
	}
	return id
}

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
	id = upstreamGenerationCatalogAdapter(id)
	w.Header().Set("Cache-Control", "no-store")
	jsonOut(w, provider.SpecializedModels(id, kind))
}

func (a *App) generationTargets(ctx context.Context) []model.GenerationTarget {
	catalog := a.modelsSnapshot()
	ctx = network.WithConfigDir(ctx, a.configDir)
	var targets []model.GenerationTarget
	for _, row := range catalog.Models {
		if row.Generation == nil {
			continue
		}
		target, key := catalog.GenerationTarget(row)
		if metadata, found := provider.SpecializedModel(upstreamGenerationCatalogAdapter(row.ProviderID), row.ID, target.Kind); found {
			target.InputModalities = metadata.Input
		}
		if key != "" {
			// Copy the saved source, not a discovery result supplied by the model.
			// Resolve renewable credentials lazily in the worker at tool dispatch.
			source, _, err := catalog.ResolveEnabled(key)
			if err != nil {
				target.Error = "The selected connection is unavailable."
			} else {
				source.Key = key
				source.CredentialRef = provider.CredentialReference(source)
				target.Connection = &source
			}
		}
		source := &target.Model
		if target.Connection != nil {
			source = target.Connection
		}
		if target.Error == "" {
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
