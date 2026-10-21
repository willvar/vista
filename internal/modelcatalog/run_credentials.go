package modelcatalog

import (
	"context"

	modelcfg "vista/internal/model"
	"vista/internal/provider"
)

// ResolveForRun resolves configuration, then refreshes only the selected
// Provider's credentials before the model is sent to a Worker.
func (c Catalog) ResolveForRun(ctx context.Context, key string) (modelcfg.Model, string, error) {
	model, selected, err := c.Resolve(key)
	if err != nil {
		return model, selected, err
	}
	return provider.ResolveRunCredentials(ctx, model, selected)
}
