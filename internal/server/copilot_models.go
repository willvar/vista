package server

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"
	"vista/internal/modelcatalog"
)

func copilotSources(catalog modelCatalog) []configuredModel {
	var sources []configuredModel
	seen := map[string]bool{}
	for _, model := range catalog.Models {
		if model.ProviderID == "github-copilot" && model.APIKey != "" {
			id := modelcatalog.CopilotSourceID(model)
			if !seen[id] {
				seen[id] = true
				sources = append(sources, model)
			}
		}
	}
	return sources
}

func (a *App) pollCopilotModels(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.refreshCopilotModels(ctx)
		}
	}
}

func (a *App) refreshCopilotModels(ctx context.Context) {
	type result struct {
		source configuredModel
		models []configuredModel
		err    error
	}
	a.modelMu.Lock()
	sources := copilotSources(a.baseModels)
	tickets := make(map[string]uint64, len(sources))
	for _, source := range sources {
		id := modelcatalog.CopilotSourceID(source)
		tickets[id] = a.beginModelDiscoveryLocked("copilot:" + id)
	}
	a.modelMu.Unlock()
	results := make(chan result, len(sources))
	var group sync.WaitGroup
	parallel := make(chan struct{}, 8)
	for _, source := range sources {
		group.Add(1)
		go func() {
			defer group.Done()
			select {
			case parallel <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-parallel }()
			models, err := modelcatalog.DiscoverCopilotModels(ctx, source)
			results <- result{source: source, models: models, err: err}
		}()
	}
	group.Wait()
	close(results)
	a.modelMu.Lock()
	defer a.modelMu.Unlock()
	active := make(map[string]bool)
	for _, source := range copilotSources(a.baseModels) {
		active[modelcatalog.CopilotSourceID(source)] = true
	}
	for discovered := range results {
		id := modelcatalog.CopilotSourceID(discovered.source)
		if !active[id] || !a.currentModelDiscoveryLocked("copilot:"+id, tickets[id]) {
			continue
		}
		if discovered.err != nil {
			if !errors.Is(discovered.err, context.Canceled) {
				log.Printf("GitHub Copilot model discovery: %v", discovered.err)
			}
			continue // Retain this source's last successful inventory.
		}
		if a.copilotInventories == nil {
			a.copilotInventories = map[string][]configuredModel{}
		}
		a.copilotInventories[modelcatalog.CopilotSourceID(discovered.source)] = discovered.models
	}
	a.rebuildModelsLocked()
}
