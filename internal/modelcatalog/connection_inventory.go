package modelcatalog

import (
	"crypto/sha256"
	"fmt"
	"slices"

	"vista/internal/provider"
)

// The old format put model keys and connection settings in the same row. Such a
// row supplies a connection and, when present in the current inventory, a
// stable model key. Neither resolution nor authentication of the connection
// depends on that particular model.
func (m Configured) catalogInventoryReference() bool {
	return m.Provider == "catalog" && m.ConnectionID != "" && catalogInventorySetup(m.ProviderID) && Editable(m)
}

func expandInventoriesWithOMPCatalog(raw, resolved Catalog, snapshot *provider.OMPCatalogSnapshot) Catalog {
	seenModels := map[string]bool{}
	seenConnections := map[string]bool{}
	references := map[string][]int{}
	for i, model := range resolved.Models {
		identity := Identity(model)
		if model.ModelUnavailable() && raw.Models[i].catalogInventoryReference() {
			references[identity] = append(references[identity], i)
		} else {
			seenModels[identity] = true
		}
	}
	addModel := func(model Configured, index int) {
		candidate := Catalog{Default: model.Key, Models: []Configured{model}}
		if err := resolveWithOMPCatalog(&candidate, snapshot); err != nil {
			return // This account's auth/route may not support this model.
		}
		if err := Validate(candidate); err != nil {
			return
		}
		if index >= 0 {
			resolved.Models[index] = candidate.Models[0]
		} else {
			resolved.Models = append(resolved.Models, candidate.Models[0])
		}
		for _, name := range candidate.credentialEnv {
			if !slices.Contains(resolved.credentialEnv, name) {
				resolved.credentialEnv = append(resolved.credentialEnv, name)
			}
		}
	}
	for _, connection := range raw.Models {
		if connection.Provider != "catalog" || connection.ConnectionID == "" || !catalogInventorySetup(connection.ProviderID) || seenConnections[connection.ConnectionID] {
			continue
		}
		seenConnections[connection.ConnectionID] = true
		for _, id := range snapshot.TextModelIDs(connection.ProviderID) {
			model := connection
			model.ID = id
			identity := Identity(model)
			if indices := references[identity]; len(indices) > 0 {
				for _, index := range indices {
					addModel(raw.Models[index], index)
				}
				continue
			}
			if seenModels[identity] {
				continue
			}
			hash := sha256.Sum256([]byte(connection.ConnectionID + "\x00" + id))
			model.Key = fmt.Sprintf("remote-%x", hash[:10])
			addModel(model.WithDiscoverySource("provider"), -1)
			seenModels[identity] = true
		}
	}
	return resolved
}
