package server

import (
	"context"
	"errors"
	"maps"
	"net/http"
	"slices"
	"strings"

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
	if edit.Model != "" {
		a.modelMu.RLock()
		base, inventories := a.baseModels, maps.Clone(a.customInventories)
		a.modelMu.RUnlock()
		choice, ok := findGenerationOption(base, inventories, edit.Kind, edit.Connection, edit.Model)
		if !ok || edit.Adapter != "" && edit.Adapter != choice.adapter {
			return errors.New("that model is no longer a selectable option for this use")
		}
		edit.Adapter = choice.adapter
	}
	raw, err := snapshot.raw.EditGeneration(edit)
	if err != nil {
		return err
	}
	a.modelMu.Lock()
	defer a.modelMu.Unlock()
	return a.commitModelSettingsLocked(ctx, snapshot, raw, false)
}

// The settings-only roster is an intersection of saved connections, output
// classes and implemented APIs. A custom chat protocol alone isn't evidence.
// Adapter identifiers are private; the user chooses the account and model.
type generationOption struct {
	Kind           string `json:"kind"`
	Connection     string `json:"connection"`
	ConnectionName string `json:"connectionName"`
	Model          string `json:"model"`
	Name           string `json:"name"`
	Workflow       bool   `json:"workflow,omitempty"`
	NoFirstFrame   bool   `json:"noFirstFrame,omitempty"`
	adapter        string
	input          []string
}

func generationOptions(base modelCatalog, inventories map[string][]modelcatalog.InventoryModel, kind string) []generationOption {
	result := []generationOption{}
	var seen []configuredModel
	for _, src := range base.Models {
		if src.Disabled || src.Generation != nil || slices.ContainsFunc(seen, func(m configuredModel) bool { return modelcatalog.SameConnection(m, src) }) {
			continue
		}
		seen = append(seen, src)
		adapterIDs := src.GenerationAdapters(kind)
		if len(adapterIDs) == 0 {
			continue
		}
		add := func(adapter string, meta provider.OutputMetadata) {
			spec, ok := generation.Find(adapter, kind)
			allowsFrame, _ := generation.VideoFrameMode(adapter, meta.ID)
			if !ok || !generation.ValidateModelID(meta.ID) {
				return
			}
			name := strings.TrimSpace(meta.Name)
			if name == "" {
				name = meta.ID
			}
			label := strings.TrimSpace(src.Name)
			if label == "" {
				label = spec.Name
			}
			result = append(result, generationOption{Kind: kind, Connection: src.GenerationConnectionRef(), ConnectionName: label, Model: meta.ID, Name: name, Workflow: adapter == "comfy", NoFirstFrame: kind == model.VideoOutput && (!allowsFrame || len(meta.Input) > 0 && !slices.Contains(meta.Input, "image")), adapter: adapter, input: meta.Input})
		}
		if src.ProviderID == "custom" || generation.NeedsInventory(src.ProviderID) {
			source, ok := customSourceFor(src)
			if !ok {
				continue
			}
			for _, remote := range inventories[source.identity()] {
				if meta, adapter, ok := remote.GenerationChoice(kind); ok && slices.Contains(adapterIDs, adapter) {
					add(adapter, meta)
				}
			}
			continue
		}
		if len(adapterIDs) != 1 {
			continue
		}
		id := adapterIDs[0]
		if src.ProviderID == "microsoft-foundry" && src.DeploymentID != "" && kind == model.ImageOutput {
			add(id, provider.OutputMetadata{ID: src.DeploymentID, Name: src.DeploymentID, Kind: kind})
			continue
		}
		for _, row := range generation.Choices(id, kind) {
			add(id, row)
		}
	}
	return result
}

func findGenerationOption(base modelCatalog, inventories map[string][]modelcatalog.InventoryModel, kind, connection, id string) (generationOption, bool) {
	for _, choice := range generationOptions(base, inventories, kind) {
		if choice.Connection == connection && choice.Model == id {
			return choice, true
		}
	}
	return generationOption{}, false
}

func (a *App) generationCandidates(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	snapshot, err := a.modelSettingsSnapshot()
	if err != nil {
		settingsHTTPError(w, err, 500)
		return
	}
	result := []generationOption{}
	if snapshot.exists {
		a.modelMu.RLock()
		base, inventories := a.baseModels, maps.Clone(a.customInventories)
		a.modelMu.RUnlock()
		for _, kind := range []string{model.ImageOutput, model.VideoOutput, model.DecisionOutput} {
			result = append(result, generationOptions(base, inventories, kind)...)
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	jsonOut(w, result)
}

func (a *App) generationTargets(ctx context.Context) []model.GenerationTarget {
	// Media keeps using the saved account even if discovery replaces its chat anchor.
	a.modelMu.RLock()
	catalog, inventories := a.baseModels, maps.Clone(a.customInventories)
	a.modelMu.RUnlock()
	ctx = network.WithConfigDir(ctx, a.configDir)
	var targets []model.GenerationTarget
	for _, row := range catalog.Models {
		if row.Generation == nil {
			continue
		}
		target := catalog.GenerationTarget(row)
		if target.Connection != nil && target.Connection.ProviderID == "custom" {
			if choice, ok := findGenerationOption(catalog, inventories, target.Kind, row.Generation.Connection, row.ID); ok && choice.adapter == row.ProviderID {
				target.InputModalities = choice.input
			}
		} else if metadata, found := provider.SpecializedModel(generation.CatalogProviderID(row.ProviderID), row.ID, target.Kind); found {
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
