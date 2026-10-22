package server

import (
	"context"
	"errors"
	"log"
	"os"
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
		ticket := a.beginModelDiscoveryLocked("local:" + source.Kind())
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
		if !a.currentModelDiscoveryLocked("local:"+source.Kind(), ticket) {
			a.modelMu.Unlock()
			continue
		}
		if a.localModels == nil {
			a.localModels = make(map[string][]configuredModel)
		}
		a.localModels[source.Kind()] = models
		a.rebuildModelsLocked()
		a.modelMu.Unlock()
	}
}

func (a *App) rebuildModelsLocked() {
	catalog := modelCatalog{Default: a.baseModels.Default, Models: []configuredModel{}}
	seenKeys := make(map[string]bool, len(a.baseModels.Models))
	seenModels := make(map[string]bool, len(a.baseModels.Models))
	copilot := make(map[string]configuredModel)
	for _, models := range a.copilotInventories {
		for _, model := range models {
			copilot[modelcatalog.Identity(model)] = model
		}
	}
	custom := make(map[string]configuredModel)
	for _, source := range customSources(a.baseModels) {
		for _, model := range a.customInventories[source.identity()] {
			custom[modelcatalog.Identity(model)] = model
		}
	}
	for _, model := range a.baseModels.Models {
		if model.ProviderID == "github-copilot" {
			if _, ready := a.copilotInventories[modelcatalog.CopilotSourceID(model)]; ready {
				if remote, ok := copilot[modelcatalog.Identity(model)]; ok {
					remote.Key = model.Key
					if model.Tools != nil && !*model.Tools {
						remote.Tools = model.Tools
					}
					if model.Context > 0 && model.Context < remote.Context {
						remote.Context = model.Context
					}
					model = remote
				} else {
					continue
				}
			}
		}
		if source, ok := customSourceFor(model); ok {
			if _, ready := a.customInventories[source.identity()]; ready {
				// A successful inventory replaces saved anchors, even when empty.
				remote, present := custom[modelcatalog.Identity(model)]
				if !present {
					continue
				}
				if model.ProviderID == "custom" {
					remote.Key = model.Key
					model = remote
				}
			}
		}
		catalog.Models = append(catalog.Models, model)
		seenKeys[model.Key] = true
		seenModels[modelcatalog.Identity(model)] = true
	}
	for _, kind := range []string{"ollama", "lmstudio", "vllm"} {
		for _, model := range a.localModels[kind] {
			managed := false
			for _, configured := range a.baseModels.Models {
				if configured.ConnectionID != "" && configured.ProviderID == kind && strings.TrimRight(configured.BaseURL, "/") == strings.TrimRight(model.BaseURL, "/") {
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
		for _, model := range a.customInventories[source.identity()] {
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
		for _, model := range a.copilotInventories[modelcatalog.CopilotSourceID(source)] {
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
	a.models = catalog
}
