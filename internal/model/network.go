package model

import (
	"context"

	"vista/internal/network"
)

// BindNetwork selects the connection's route once, or restores its execution
// snapshot. Credential caching, renewal and inference share this boundary.
func (m Model) BindNetwork(ctx context.Context) (context.Context, Model, error) {
	var err error
	if m.Network != nil {
		if err = m.Network.Validate(); err != nil {
			return ctx, m, err
		}
		ctx = network.WithConnectionPolicy(ctx, *m.Network)
	} else {
		ctx, err = network.ForConnection(ctx, m.ProxyID)
		if err != nil {
			return ctx, m, err
		}
	}
	policy, err := network.ConnectionPolicy(ctx)
	if err != nil {
		return ctx, m, err
	}
	m.Network = &policy
	return ctx, m, nil
}
