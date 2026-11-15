package server

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log"
	"time"
	"vista/internal/modelcatalog"
)

// Connected providers supply their own live model inventory.
// The connection stays in config.json models; discovered IDs stay in memory so a
// provider with a large catalog does not rewrite the user's configuration.
type customSource struct {
	model configuredModel
}

func (source customSource) identity() string {
	// Inventory access depends on service/account identity, not run preferences.
	model := source.model
	data, _ := json.Marshal(struct {
		ConnectionID, Provider, ProviderID, BaseURL, Authentication, APIKey string
		Headers                                                             map[string]string `json:"headers,omitempty"`
	}{model.ConnectionID, model.Provider, model.ProviderID, model.BaseURL, model.Authentication, model.APIKey, model.Headers})
	return fmt.Sprintf("%x", sha256.Sum256(data))
}

func customSourceFor(model configuredModel) (customSource, bool) {
	zen := modelcatalog.ZenProvider(model.ProviderID)
	local := modelcatalog.LocalProvider(model.ProviderID) && model.ConnectionID != ""
	custom := modelcatalog.SetupID(model) == "custom" && model.BaseURL != ""
	if !local && !custom && !(zen && model.APIKey != "") {
		return customSource{}, false
	}
	template := model.DiscoveryTemplate()
	if zen {
		// Resolve each Zen model's native endpoint and route independently.
		template.Provider, template.BaseURL, template.ResponsesRoute = "catalog", "", ""
	}
	return customSource{model: template}, true
}

func customSources(catalog modelCatalog) []customSource {
	seen := map[string]bool{}
	sources := make([]customSource, 0)
	for _, model := range catalog.Models {
		source, ok := customSourceFor(model)
		if !ok {
			continue
		}
		if !seen[source.identity()] {
			seen[source.identity()] = true
			sources = append(sources, source)
		}
	}
	return sources
}

func (a *App) pollCustomModels(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.refreshCustomModels(ctx)
		}
	}
}

func (a *App) refreshCustomModels(ctx context.Context) {
	a.modelMu.Lock()
	sources := customSources(a.baseModels)
	tickets := make(map[string]uint64, len(sources))
	for _, source := range sources {
		tickets[source.identity()] = a.beginModelDiscoveryLocked("custom:" + source.identity())
	}
	a.modelMu.Unlock()
	for _, source := range sources {
		if ctx.Err() != nil {
			return
		}
		requestCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		modelsFound, err := modelcatalog.DiscoverModels(requestCtx, source.model)
		ids := make([]string, 0, len(modelsFound))
		local := map[string]configuredModel{}
		for _, model := range modelsFound {
			ids = append(ids, model.ID)
			local[model.ID] = model
		}
		cancel()
		if err != nil {
			// Keep the last successful inventory while a provider is unavailable.
			log.Printf("model discovery for %s: %v", source.model.Name, err)
			continue
		}
		models := make([]modelcatalog.InventoryModel, 0, len(ids))
		for _, id := range ids {
			if modelcatalog.ZenProvider(source.model.ProviderID) && !modelcatalog.SelectableZenTextModel(source.model.ProviderID, id) {
				continue
			}
			metadata := modelcatalog.InventoryModel{ID: id}
			if remote, ok := local[id]; ok {
				metadata = remote.InventoryMetadata()
			}
			models = append(models, metadata)
		}
		a.modelMu.Lock()
		if !a.currentModelDiscoveryLocked("custom:"+source.identity(), tickets[source.identity()]) {
			a.modelMu.Unlock()
			continue
		}
		for _, current := range customSources(a.baseModels) {
			if current.identity() == source.identity() {
				if a.customInventories == nil {
					a.customInventories = map[string][]modelcatalog.InventoryModel{}
				}
				a.customInventories[source.identity()] = models
				a.rebuildModelsLocked()
				break
			}
		}
		a.modelMu.Unlock()
	}
}
