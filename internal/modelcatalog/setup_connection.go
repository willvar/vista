package modelcatalog

import (
	"context"
	"errors"
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
	standalone := len(ids) == 0 && setup.ID != "azure"
	if len(ids) == 0 && previous != nil {
		// Keep saved model keys/choices on edits, even if a model has been
		// withdrawn. They are not needed to connect or to discover models.
		for _, model := range raw.Models {
			if SameConnection(*previous, model) {
				ids = append(ids, model.ID)
			}
		}
	}
	if standalone && len(ids) == 0 {
		ids = []string{""}
	}
	if len(ids) == 0 || len(ids) > 16 {
		return Catalog{}, errors.New("choose between 1 and 16 models")
	}
	discovered := map[string]Configured{}
	if len(input.IDs) != 0 && (LocalProvider(setup.ID) || setup.ID == "github-copilot") {
		models, err := DiscoverModels(ctx, prototype)
		if err != nil {
			return Catalog{}, errors.New("could not load this provider's models: " + err.Error())
		}
		for _, model := range models {
			discovered[model.ID] = model
		}
	}
	if ZenProvider(setup.ID) && len(input.IDs) != 0 {
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
		if id == "" && !standalone || strings.TrimSpace(id) != id || len(id) > 200 || seen[id] {
			return Catalog{}, errors.New("model IDs must be unique and at most 200 characters")
		}
		seen[id] = true
		model := prototype
		model.ID, model.ConnectionID = id, connectionID
		if standalone {
			if saved, ok := old[id]; ok && id != "" && (LocalProvider(setup.ID) || setup.ID == "github-copilot") {
				model.Provider, model.Tools, model.Context = saved.Provider, saved.Tools, saved.Context
			}
		} else if LocalProvider(setup.ID) || setup.ID == "github-copilot" {
			remote, ok := discovered[id]
			if !ok {
				return Catalog{}, errors.New("selected model is absent from the provider's model list")
			}
			model.Provider, model.BaseURL = remote.Provider, remote.BaseURL
			model.Tools, model.Context = remote.Tools, remote.Context
		} else if model.Provider == "catalog" {
			_, err := provider.ResolveCatalogConfig(model.providerConfig())
			if err != nil {
				return Catalog{}, errors.New("choose a model supported by this provider's catalog")
			}
		}
		if saved, ok := old[id]; ok {
			model.Key = saved.Key
		} else {
			keyName := id
			if keyName == "" {
				keyName = "connection"
			}
			model.Key = GeneratedModelKey(input.Name, keyName, used)
			used[model.Key] = true
		}
		if firstReplacement == "" && id != "" {
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
		if replacingDefault && !foundDefault {
			raw.Default = firstReplacement
		}
	}
	return raw, nil
}
