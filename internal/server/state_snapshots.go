package server

import (
	"context"
	"encoding/json"
)

type publicModelCatalog struct {
	Default string      `json:"default"`
	Models  []modelInfo `json:"models"`
}

func (a *App) publicModelsSnapshot() publicModelCatalog {
	models := a.modelsSnapshot()
	return publicModelCatalog{models.Default, models.Public()}
}

func (a *App) addStateSnapshots(ctx context.Context, point *stateCheckpoint, previous stateCheckpoint) error {
	changed := func(topic string) bool { return point.Revisions[topic] != previous.Revisions[topic] }
	if changed("models") {
		models := a.publicModelsSnapshot()
		point.Models = &models
	}
	if changed("catalog") {
		catalog := a.modelCatalogInfo()
		point.Catalog = &catalog
	}
	if changed("provider-login") {
		logins := a.providerLoginUpdates()
		point.ProviderLogins = &logins
	}
	if changed("mcp") {
		mcp := a.mcpStateSnapshot()
		point.MCP = &mcp
	}
	if changed("storage") {
		storage := a.resourceStorageSnapshot()
		point.Storage = &storage
	}
	if changed("memories") {
		memories, err := a.memoriesSnapshot(ctx)
		if err != nil {
			return err
		}
		point.Memories = memories
	}
	if changed("diagnostics") {
		point.Diagnostics = a.updates.diagnosticChanges(previous.Revisions["diagnostics"], point.Revisions["diagnostics"])
	}
	return nil
}

// Preserve revisions when omitting large payloads: the browser repairs only
// those missing snapshots over HTTP. Normal heartbeats repeat no payloads.
func (point *stateCheckpoint) limitPayload() error {
	data, err := json.Marshal(point)
	if err != nil || len(data) <= maxStateSnapshotBytes {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	omit := map[string]func(){
		"models": func() { point.Models = nil }, "diagnostics": func() { point.Diagnostics = nil },
		"sessions": func() { point.Sessions = nil }, "memories": func() { point.Memories = nil },
		"mcp": func() { point.MCP = nil }, "catalog": func() { point.Catalog = nil },
		"providerLogins": func() { point.ProviderLogins = nil }, "storage": func() { point.Storage = nil },
		"status": func() { point.Status = nil },
	}
	for len(data) > maxStateSnapshotBytes {
		largest := ""
		for key := range omit {
			if len(fields[key]) > len(fields[largest]) {
				largest = key
			}
		}
		if largest == "" {
			break
		}
		omit[largest]()
		delete(omit, largest)
		delete(fields, largest)
		data, err = json.Marshal(fields)
		if err != nil {
			return err
		}
	}
	return nil
}
