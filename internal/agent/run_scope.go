package agent

import (
	"fmt"
	"sync/atomic"

	"vista/internal/agentconfig"
	"vista/internal/appdirs"
)

type runScope struct {
	modelTurns atomic.Int32
	// Read once at the root run's start and shared unchanged by all descendants.
	delegationDepth int
	// A separate flag preserves the difference between an explicit zero and an
	// uninitialized runScope used by tests or internal callers.
	delegationDepthSet bool
	delegations        *delegationRegistry
}

func (s *runScope) delegationLimit() int {
	if s == nil || !s.delegationDepthSet {
		return agentconfig.DefaultDelegationDepth
	}
	return s.delegationDepth
}

func newRunScope(delegations *delegationRegistry) (*runScope, error) {
	configDir, err := appdirs.Config()
	if err != nil {
		return nil, err
	}
	settings, err := agentconfig.ReadSettings(configDir)
	if err != nil {
		return nil, fmt.Errorf("read agent settings: %w", err)
	}
	if delegations == nil {
		delegations = newDelegationRegistry()
	}
	return &runScope{delegationDepth: settings.DelegationDepth, delegationDepthSet: true, delegations: delegations}, nil
}
