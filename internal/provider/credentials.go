package provider

import (
	"context"

	modelcfg "vista/internal/model"
)

// ResolveRunCredentials exchanges long-lived credentials in the Server. The
// credential cache calls it on initial use and renewal. Provider authentication takes precedence
// over the protocol's default authentication.
func ResolveRunCredentials(ctx context.Context, model modelcfg.Model, selected string) (modelcfg.Model, string, error) {
	if prepare := catalogAdapterFor(model.ProviderID).prepareRun; prepare != nil {
		return prepare(ctx, model, selected)
	}
	if prepare := protocolAdapters[model.Provider].prepareRun; prepare != nil {
		return prepare(ctx, model, selected)
	}
	return model, selected, nil
}
