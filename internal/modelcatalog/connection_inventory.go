package modelcatalog

import (
	"crypto/sha256"
	"fmt"
	"slices"

	"vista/internal/provider"
)

// expandConnectionInventories adds the built-in model inventory in memory.
// Persisted entries retain connection credentials and the chosen default; they
// are not a whitelist of the provider's models. Resolve every model from the
// raw connection so one model's derived route or capabilities cannot leak into
// another model behind the same provider.
func expandConnectionInventories(raw, resolved Catalog) Catalog {
	return expandInventoriesWithOMPCatalog(raw, resolved, provider.CurrentOMPCatalog())
}

func expandInventoriesWithOMPCatalog(raw, resolved Catalog, snapshot *provider.OMPCatalogSnapshot) Catalog {
	seenModels := map[string]bool{}
	seenConnections := map[string]bool{}
	for _, model := range resolved.Models {
		seenModels[Identity(model)] = true
	}
	for _, connection := range raw.Models {
		if connection.Provider != "catalog" || connection.ConnectionID == "" || !catalogInventorySetup(connection.ProviderID) || seenConnections[connection.ConnectionID] {
			continue
		}
		seenConnections[connection.ConnectionID] = true
		for _, id := range snapshot.TextModelIDs(connection.ProviderID) {
			model := connection
			model.ID = id
			if seenModels[Identity(model)] {
				continue
			}
			hash := sha256.Sum256([]byte(connection.ConnectionID + "\x00" + id))
			model.Key = fmt.Sprintf("remote-%x", hash[:10])
			model = model.WithDiscoverySource("provider")
			candidate := Catalog{Default: model.Key, Models: []Configured{model}}
			if err := resolveWithOMPCatalog(&candidate, snapshot); err != nil {
				continue
			}
			if err := Validate(candidate); err != nil {
				continue
			}
			resolved.Models = append(resolved.Models, candidate.Models[0])
			seenModels[Identity(model)] = true
			for _, name := range candidate.credentialEnv {
				if !slices.Contains(resolved.credentialEnv, name) {
					resolved.credentialEnv = append(resolved.credentialEnv, name)
				}
			}
		}
	}
	return resolved
}
