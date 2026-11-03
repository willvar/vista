package server

import (
	"context"
	"encoding/json"
	"strconv"

	"vista/internal/store"
)

type publicModelCatalog struct {
	Default                string      `json:"default"`
	Models                 []modelInfo `json:"models"`
	ShowUnrecognizedModels bool        `json:"showUnrecognizedModels"`
	SettingsRevision       string      `json:"settingsRevision"`
}

// Distinguish persisted connection edits from inventory/preference updates.
// The server epoch prevents a restarted process from reusing an old version.
func (a *App) modelSettingsVersion(revision uint64) string {
	return a.updates.snapshot().Epoch + ":" + strconv.FormatUint(revision, 10)
}

func (a *App) publicModelsSnapshot(ctx context.Context) (publicModelCatalog, error) {
	prefs, err := a.store.Preferences(ctx)
	if err != nil {
		return publicModelCatalog{}, err
	}
	a.modelMu.RLock()
	models, revision := a.models, a.modelRevision
	a.modelMu.RUnlock()
	return publicModelCatalog{Default: initialVisibleModelKey(models, prefs.ShowUnrecognizedModels), Models: models.Public(), ShowUnrecognizedModels: prefs.ShowUnrecognizedModels, SettingsRevision: a.modelSettingsVersion(revision)}, nil
}

func (a *App) addStateSnapshots(ctx context.Context, point *stateCheckpoint, previous stateCheckpoint) error {
	changed := func(topic string) bool { return point.Revisions[topic] != previous.Revisions[topic] }
	if changed("models") {
		models, err := a.publicModelsSnapshot(ctx)
		if err != nil {
			return err
		}
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
		total, err := a.store.MemoryCount(ctx)
		if err != nil {
			return err
		}
		// Contents belong to the manager's requested page, not every browser
		// with an application stream open. A revision wakes only active views.
		point.Memories = &memorySummary{total, store.MaxMemories, store.MaxMemoryBytes, store.MaxMemoriesBytes}
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
