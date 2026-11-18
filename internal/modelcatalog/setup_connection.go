package modelcatalog

import (
	"context"
	"errors"
	"maps"
	"strings"

	"github.com/google/uuid"
	"vista/internal/provider"
)

func replaceConnection(ctx context.Context, raw Catalog, previous *Configured, setup ProviderSetup, input ConnectionInput) (Catalog, error) {
	connectionID := uuid.NewString()
	old := map[string]Configured{}
	retained := make([]Configured, 0, len(raw.Models))
	used := map[string]bool{}
	replacingDefault := false
	for _, model := range raw.Models {
		used[model.Key] = true
		if previous != nil && SameConnection(*previous, model) {
			if !Editable(model) {
				return Catalog{}, errors.New("provider has advanced settings managed outside the WebUI")
			}
			old[model.ID] = model
			replacingDefault = replacingDefault || raw.Default == model.Key
			if previous.ConnectionID != "" {
				connectionID = previous.ConnectionID
			}
		} else {
			retained = append(retained, model)
		}
	}
	prototype := input.configured()
	if previous != nil {
		prototype.Disabled = previous.Disabled
	}
	ids := input.IDs
	if previous != nil && len(ids) == 0 && (setup.ID != "custom" || previous.Provider == input.Provider && previous.Authentication == input.CredentialMode && previous.BaseURL == input.BaseURL && previous.APIKey == input.APIKey && maps.Equal(previous.Headers, input.Headers)) {
		// Keep existing anchors that the edited authentication mode can use.
		for _, model := range raw.Models {
			if SameConnection(*previous, model) {
				if catalogInventorySetup(setup.ID) {
					candidate := prototype
					candidate.ID = model.ID
					if _, err := provider.ResolveCatalogConfig(candidate.providerConfig()); err != nil {
						continue
					}
				}
				ids = append(ids, model.ID)
			}
		}
	}
	discovered := map[string]Configured{}
	if LocalProvider(setup.ID) || setup.ID == "github-copilot" {
		var models []Configured
		var err error
		models, err = DiscoverModels(ctx, prototype)
		if err != nil {
			return Catalog{}, errors.New("could not load this provider's models: " + err.Error())
		}
		for _, model := range models {
			discovered[model.ID] = model
		}
		if len(models) == 0 {
			return Catalog{}, errors.New("provider did not return any usable text models")
		}
		if previous != nil && len(input.IDs) == 0 {
			kept := ids[:0]
			for _, id := range ids {
				if _, ok := discovered[id]; ok {
					kept = append(kept, id)
				}
			}
			ids = kept
		}
		if len(ids) == 0 && len(models) > 0 {
			ids = []string{models[0].ID}
		}
	}
	if len(ids) == 0 && (setup.ID == "custom" || ZenProvider(setup.ID)) {
		var err error
		if ZenProvider(setup.ID) {
			var models []Configured
			models, err = DiscoverModels(ctx, prototype)
			for _, model := range models {
				ids = append(ids, model.ID)
			}
			filtered := make([]string, 0, len(ids))
			for _, id := range ids {
				if SelectableZenTextModel(setup.ID, id) {
					filtered = append(filtered, id)
				}
			}
			ids = filtered
		} else {
			var models []Configured
			models, err = DiscoverModels(ctx, prototype)
			for _, model := range models {
				ids = append(ids, model.ID)
				discovered[model.ID] = model
			}
		}
		if err != nil || len(ids) == 0 {
			message := "provider returned an empty model list"
			if err != nil {
				message = err.Error()
			}
			return Catalog{}, NewSettingsError("model_ids_required", message, nil)
		}
		ids = ids[:1] // Persist an anchor; the live inventory is kept in memory.
	}
	if len(ids) == 0 && catalogInventorySetup(setup.ID) {
		var routeErr error
		for _, id := range provider.CatalogTextModelIDs(setup.ID) {
			candidate := prototype
			candidate.ID = id
			// An account's authentication mode may support only part of a
			// provider's catalog (for example Vertex Express Mode).
			if _, err := provider.ResolveCatalogConfig(candidate.providerConfig()); err != nil {
				routeErr = err
				continue
			}
			ids = []string{id}
			break
		}
		if len(ids) == 0 {
			if routeErr != nil {
				return Catalog{}, routeErr
			}
			return Catalog{}, errors.New("provider has no supported text models")
		}
	}
	if len(ids) == 0 || len(ids) > 16 {
		return Catalog{}, errors.New("choose between 1 and 16 models")
	}
	if ZenProvider(setup.ID) && len(input.IDs) > 0 {
		if err := validateZenModels(ctx, prototype, ids); err != nil {
			return Catalog{}, err
		}
	}
	if input.DeploymentID != "" && len(ids) != 1 {
		return Catalog{}, errors.New("an Azure deployment must select exactly one model")
	}
	seen := map[string]bool{}
	firstReplacement := ""
	for _, id := range ids {
		if id == "" || strings.TrimSpace(id) != id || len(id) > 200 || seen[id] {
			return Catalog{}, errors.New("model IDs must be unique and at most 200 characters")
		}
		seen[id] = true
		model := prototype
		model.ID, model.ConnectionID = id, connectionID
		if LocalProvider(setup.ID) || setup.ID == "github-copilot" {
			remote, ok := discovered[id]
			if !ok {
				return Catalog{}, errors.New("selected model is absent from the provider's model list")
			}
			model.Provider, model.BaseURL = remote.Provider, remote.BaseURL
			model.Tools, model.Context = remote.Tools, remote.Context
		} else if model.Provider == "catalog" {
			entry, err := provider.ResolveCatalogModel(model.ProviderID, id)
			if err != nil || entry.Protocol == "" {
				return Catalog{}, errors.New("choose a model supported by this provider's catalog")
			}
		}
		if saved, ok := old[id]; ok {
			model.Key = saved.Key
		} else {
			model.Key = GeneratedModelKey(input.Name, id, used)
			used[model.Key] = true
		}
		if firstReplacement == "" {
			firstReplacement = model.Key
		}
		retained = append(retained, model)
	}
	// Older bindings used a model anchor rather than the connection identity.
	// The provider editor can replace that anchor while keeping the account.
	for i, row := range retained {
		if row.Generation == nil {
			continue
		}
		for _, before := range old {
			if row.Generation.Connection == before.Key {
				binding := *row.Generation
				binding.Connection = connectionID
				retained[i].Generation = &binding
				break
			}
		}
	}
	raw.Models = retained
	foundDefault := false
	for _, model := range retained {
		foundDefault = foundDefault || model.Key == raw.Default
	}
	if !foundDefault {
		raw.Default = firstReplacement
		for _, row := range retained {
			if row.ChatEligible() {
				raw.Default = row.Key
				break
			}
		}
		if replacingDefault {
			raw.Default = firstReplacement
		}
	}
	return raw, nil
}
