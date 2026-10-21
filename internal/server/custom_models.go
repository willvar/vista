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
// The connection stays in models.json; discovered IDs stay in memory so a
// provider with a large catalog does not rewrite the user's configuration.
type customSource struct {
	connectionID, name, provider, providerID, baseURL, apiKey, authentication string
	headers                                                                   map[string]string
}

func (source customSource) identity() string {
	if len(source.headers) == 0 {
		source.headers = nil
	}
	data, _ := json.Marshal([]any{source.connectionID, source.name, source.provider, source.providerID, source.baseURL, source.apiKey, source.authentication, source.headers})
	return fmt.Sprintf("%x", sha256.Sum256(data))
}

func customSourceFor(model configuredModel) (customSource, bool) {
	zen := modelcatalog.ZenProvider(model.ProviderID)
	local := modelcatalog.LocalProvider(model.ProviderID) && model.ConnectionID != ""
	custom := modelcatalog.SetupID(model) == "custom" && model.BaseURL != ""
	if !local && !custom && !(zen && model.APIKey != "") {
		return customSource{}, false
	}
	source := customSource{connectionID: model.ConnectionID, name: model.Name, provider: model.Provider, providerID: model.ProviderID, baseURL: model.BaseURL, apiKey: model.APIKey, authentication: model.Authentication, headers: model.Headers}
	if zen {
		source.provider, source.baseURL = "catalog", ""
	}
	return source, true
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
		var ids []string
		var err error
		local := map[string]configuredModel{}
		if modelcatalog.LocalProvider(source.providerID) {
			var models []configuredModel
			models, err = modelcatalog.DiscoverConfiguredLocalModels(requestCtx, configuredModel{ProviderID: source.providerID, BaseURL: source.baseURL, APIKey: source.apiKey})
			for _, model := range models {
				ids = append(ids, model.ID)
				local[model.ID] = model
			}
		} else if modelcatalog.ZenProvider(source.providerID) {
			ids, err = modelcatalog.FetchZenModelIDs(requestCtx, source.providerID, source.apiKey)
		} else {
			var models []configuredModel
			models, err = modelcatalog.DiscoverCustomModels(requestCtx, configuredModel{ConnectionID: source.connectionID, Name: source.name, Provider: source.provider, ProviderID: source.providerID, BaseURL: source.baseURL, APIKey: source.apiKey, Authentication: source.authentication, Headers: source.headers})
			for _, model := range models {
				ids = append(ids, model.ID)
				local[model.ID] = model
			}
		}
		cancel()
		if err != nil {
			// Keep the last successful inventory while a provider is unavailable.
			log.Printf("model discovery for %s: %v", source.name, err)
			continue
		}
		models := make([]configuredModel, 0, len(ids))
		for _, id := range ids {
			if modelcatalog.ZenProvider(source.providerID) && !modelcatalog.SelectableZenTextModel(source.providerID, id) {
				continue
			}
			identity := source.connectionID
			if identity == "" {
				identity = source.provider + "\x00" + source.providerID + "\x00" + source.baseURL
			}
			hash := sha256.Sum256([]byte(identity + "\x00" + id))
			model := configuredModel{
				Key: fmt.Sprintf("remote-%x", hash[:10]), ConnectionID: source.connectionID, Name: source.name, Provider: source.provider,
				ProviderID: source.providerID, ID: id, BaseURL: source.baseURL, APIKey: source.apiKey, Authentication: source.authentication, Headers: source.headers,
			}.WithDiscoverySource("provider")
			if remote, ok := local[id]; ok {
				if modelcatalog.LocalProvider(source.providerID) {
					model.Tools, model.Context = remote.Tools, remote.Context
				} else {
					remote.Key = model.Key
					model = remote.WithDiscoverySource("provider")
				}
			}
			resolved := modelCatalog{Models: []configuredModel{model}}
			if err := modelcatalog.ResolveCatalog(&resolved); err == nil {
				models = append(models, resolved.Models[0])
			}
		}
		a.modelMu.Lock()
		if !a.currentModelDiscoveryLocked("custom:"+source.identity(), tickets[source.identity()]) {
			a.modelMu.Unlock()
			continue
		}
		for _, current := range customSources(a.baseModels) {
			if current.identity() == source.identity() {
				if a.customInventories == nil {
					a.customInventories = map[string][]configuredModel{}
				}
				a.customInventories[source.identity()] = models
				a.rebuildModelsLocked()
				break
			}
		}
		a.modelMu.Unlock()
	}
}
