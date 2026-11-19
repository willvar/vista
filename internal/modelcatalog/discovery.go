package modelcatalog

import (
	"context"
	"errors"

	"vista/internal/generation"
	"vista/internal/network"
	"vista/internal/provider"
)

// ConnectionContext binds all operations on a complete saved/draft connection.
// The connection's empty ProxyID explicitly selects Direct, never Vista global.
func (m Configured) ConnectionContext(ctx context.Context) (context.Context, error) {
	return network.ForConnection(ctx, m.ProxyID)
}

// DiscoverModels is the only entry for connection-owned model discovery. Keep
// account and network settings together before selecting a provider's protocol.
func DiscoverModels(ctx context.Context, source Configured) ([]Configured, error) {
	ctx, err := source.ConnectionContext(ctx)
	if err != nil {
		return nil, err
	}
	switch {
	case source.ProviderID == "github-copilot":
		return discoverCopilotModels(ctx, source)
	case LocalProvider(source.ProviderID):
		models, err := discoverLocalModels(ctx, LocalSource{kind: source.ProviderID, baseURL: source.BaseURL, apiKey: source.APIKey})
		for index := range models {
			models[index].ProxyID = source.ProxyID
		}
		return models, err
	case ZenProvider(source.ProviderID):
		ids, err := fetchZenModelIDs(ctx, source)
		models := make([]Configured, 0, len(ids))
		for _, id := range ids {
			model := source
			model.ID = id
			models = append(models, model)
		}
		return models, err
	case SetupID(source) == "custom":
		return discoverCustomModels(ctx, source)
	default:
		return nil, errors.New("provider has no model discovery endpoint")
	}
}

// DiscoverConnectionInventory keeps media declarations for the settings picker.
// Conversation setup continues to consume only the chat projection.
func DiscoverConnectionInventory(ctx context.Context, source Configured) ([]Configured, error) {
	if generation.NeedsInventory(source.ProviderID) {
		request := provider.CatalogModelRequest(source.providerConfig(), source.providerMetadata())
		request.ProxyID = source.ProxyID
		rows, err := generation.DiscoverInventory(ctx, request)
		result := make([]Configured, 0, len(rows))
		for _, row := range rows {
			found := source.DiscoveryTemplate()
			found.ID, found.modelKind, found.discoveredName, found.inputModalities = row.ID, row.Kind, row.Name, row.Input
			found.mediaAdapter = source.ProviderID
			result = append(result, found)
		}
		return result, err
	}
	if SetupID(source) != "custom" {
		return DiscoverModels(ctx, source)
	}
	ctx, err := source.ConnectionContext(ctx)
	if err != nil {
		return nil, err
	}
	return discoverCustomInventory(ctx, source)
}
