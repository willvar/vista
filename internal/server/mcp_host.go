package server

import (
	"os"
	"time"

	"vista/internal/mcphost"
	"vista/internal/modelcatalog"
	"vista/internal/workerproto"
)

func (a *App) startMCPHost() {
	a.mcpHost = mcphost.NewProcess(mcphost.ProcessOptions{
		ConfigDir: a.configDir, StateDir: a.dir,
		Env: modelcatalog.WorkerEnvironment(os.Environ(), a.modelCredentialEnvironment()...),
		Snapshot: func() ([]mcphost.Definition, int64) {
			policy, err := a.globalNetworkPolicy()
			limit := int64(a.memorySettings().WorkerMiB) << 20
			if err != nil {
				return nil, limit
			}
			servers, searches := a.currentMCPServers(), a.currentSearchProviders()
			pinMCPNetwork(policy, servers, searches)
			return mcphost.Definitions(servers, searches), limit
		},
		State: func(event mcphost.Event) {
			policy, policyErr := a.globalNetworkPolicy()
			a.mcpMu.Lock()
			index := a.mcpIndexLocked(event.ID)
			config := workerproto.MCPServer{}
			if index >= 0 {
				config = a.mcpServers[index]
				if config.Transport == "http" && policyErr == nil {
					config.Network = &policy
				}
			}
			if index >= 0 && !config.Disabled && policyErr == nil && mcphost.Key(mcphost.Definition{Config: config}) == event.Key {
				if a.mcpChecks == nil {
					a.mcpChecks = map[string]mcpCheck{}
				}
				a.mcpChecks[event.ID] = mcpCheck(event.State)
			}
			a.mcpMu.Unlock()
			a.updates.notify("mcp")
		},
		Stopped: func() {
			a.mcpMu.Lock()
			if a.mcpChecks == nil {
				a.mcpChecks = map[string]mcpCheck{}
			}
			for _, config := range a.mcpServers {
				if !config.Disabled {
					a.mcpChecks[config.ID] = mcpCheck{Status: "error", CheckedAt: time.Now().UTC(), Error: "MCP host interrupted; reconnecting. Interrupted calls are not replayed."}
				}
			}
			a.mcpMu.Unlock()
			a.updates.notify("mcp", "status")
		},
	})
	a.background.Go(func() { a.mcpHost.Run(a.ctx) })
}

func (a *App) refreshMCPHost() {
	if a.mcpHost != nil {
		a.mcpHost.Refresh()
	}
}
