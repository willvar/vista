package server

import (
	"context"
	"errors"
	"log"
	"os"
	"reflect"
	"strings"
	"time"
	"vista/internal/modelcatalog"
)

func (a *App) modelsSnapshot() modelCatalog {
	a.modelMu.RLock()
	defer a.modelMu.RUnlock()
	return a.models
}

func (a *App) pollLocalModels(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.refreshLocalModels(ctx)
		}
	}
}

func (a *App) refreshLocalModels(ctx context.Context) {
	for _, source := range modelcatalog.LocalSources() {
		a.modelMu.Lock()
		ticket := a.beginModelDiscoveryLocked("local:" + source.Identity())
		a.modelMu.Unlock()
		models, err := modelcatalog.DiscoverLocalModels(ctx, source)
		if err != nil {
			// A transient outage retains the last successful inventory.
			name := "VISTA_" + strings.ToUpper(source.Kind()) + "_BASE_URL"
			if os.Getenv(name) != "" && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
				log.Printf("%s model discovery: %v", source.Kind(), err)
			}
			continue
		}
		a.modelMu.Lock()
		if !a.currentModelDiscoveryLocked("local:"+source.Identity(), ticket) {
			a.modelMu.Unlock()
			continue
		}
		if a.localModels == nil {
			a.localModels = make(map[string][]modelcatalog.InventoryModel)
		}
		a.localModels[source.Identity()] = modelcatalog.Inventory(models)
		a.rebuildModelsLocked()
		a.modelMu.Unlock()
	}
}

func (a *App) rebuildModelsLocked() {
	catalog := modelCatalog{Default: a.baseModels.Default, Models: []configuredModel{}}
	seenKeys := make(map[string]bool, len(a.baseModels.Models))
	seenModels := make(map[string]bool, len(a.baseModels.Models))
	for _, model := range a.baseModels.Models {
		if model.ProviderID == "github-copilot" {
			if inventory, ready := a.copilotInventories[modelcatalog.CopilotSourceID(model)]; ready {
				if remote, ok := inventoryModel(inventory, model.ID); ok {
					model = model.WithInventoryModel(remote)
				} else {
					continue
				}
			}
		}
		if source, ok := customSourceFor(model); ok {
			if inventory, ready := a.customInventories[source.identity()]; ready {
				// A successful inventory replaces saved anchors, even when empty.
				remote, present := inventoryModel(inventory, model.ID)
				if !present {
					continue
				}
				if model.ProviderID == "custom" || modelcatalog.LocalProvider(model.ProviderID) {
					model = model.WithDiscoveryMetadata(remote)
				}
			}
		}
		catalog.Models = append(catalog.Models, model)
		seenKeys[model.Key] = true
		seenModels[modelcatalog.Identity(model)] = true
	}
	for _, source := range modelcatalog.LocalSources() {
		for _, remote := range a.localModels[source.Identity()] {
			model := source.WithInventoryModel(remote)
			managed := false
			for _, configured := range a.baseModels.Models {
				if configured.ConnectionID != "" && configured.ProviderID == source.Kind() && strings.TrimRight(configured.BaseURL, "/") == strings.TrimRight(model.BaseURL, "/") {
					managed = true
					break
				}
			}
			if managed {
				continue
			}
			identity := modelcatalog.Identity(model)
			if seenKeys[model.Key] || seenModels[identity] {
				continue
			}
			catalog.Models = append(catalog.Models, model)
			seenKeys[model.Key] = true
			seenModels[identity] = true
		}
	}
	for _, source := range customSources(a.baseModels) {
		for _, remote := range a.customInventories[source.identity()] {
			// Resolve against current catalog routes and connection preferences.
			model := source.model.WithInventoryModel(remote).WithDiscoverySource("provider")
			resolved := modelCatalog{Models: []configuredModel{model}}
			if err := modelcatalog.ResolveCatalog(&resolved); err != nil {
				continue
			}
			model = resolved.Models[0]
			identity := modelcatalog.Identity(model)
			if seenKeys[model.Key] || seenModels[identity] {
				continue
			}
			catalog.Models = append(catalog.Models, model)
			seenKeys[model.Key] = true
			seenModels[identity] = true
		}
	}
	for _, source := range copilotSources(a.baseModels) {
		for _, remote := range a.copilotInventories[modelcatalog.CopilotSourceID(source)] {
			model := source.DiscoveryTemplate().WithInventoryModel(remote)
			identity := modelcatalog.Identity(model)
			if seenKeys[model.Key] || seenModels[identity] {
				continue
			}
			catalog.Models = append(catalog.Models, model)
			seenKeys[model.Key] = true
			seenModels[identity] = true
		}
	}
	if len(catalog.Models) == 0 {
		catalog.Default = ""
	} else if !seenKeys[catalog.Default] {
		catalog.Default = catalog.Models[0].Key
	}
	if !reflect.DeepEqual(a.models, catalog) {
		previous := a.models
		a.models = catalog
		a.updates.notify("models", "status")
		// Recheck durable continuations once per availability change. Run outside
		// the publication path: selection takes modelMu and rechecks admission.
		if a.ctx != nil && a.ctx.Err() == nil && a.store != nil && modelsBecameAvailable(previous, catalog) {
			a.background.Go(a.launchReadyRuntimeContinuations)
		}
	}
}

func modelsBecameAvailable(previous, current modelCatalog) bool {
	available := make(map[string]bool, len(previous.Models))
	for _, model := range previous.Models {
		available[model.Key] = model.Available()
	}
	for _, model := range current.Models {
		if model.Available() && !available[model.Key] {
			return true
		}
	}
	return false
}

func inventoryModel(models []modelcatalog.InventoryModel, id string) (modelcatalog.InventoryModel, bool) {
	for _, model := range models {
		if model.ID == id {
			return model, true
		}
	}
	return modelcatalog.InventoryModel{}, false
}
