package server

import (
	"os"
	"time"

	"vista/internal/mcphost"
	"vista/internal/modelcatalog"
)

func (a *App) startMCPHost() {
	a.mcpHost = mcphost.NewProcess(mcphost.ProcessOptions{
		ConfigDir: a.configDir, StateDir: a.dir,
		Env: modelcatalog.WorkerEnvironment(os.Environ(), a.modelCredentialEnvironment()...),
		Snapshot: func() ([]mcphost.Definition, int64) {
			return mcphost.Definitions(a.currentMCPServers(), a.currentSearchProviders()), int64(a.memorySettings().WorkerMiB) << 20
		},
		State: func(event mcphost.Event) {
			a.mcpMu.Lock()
			index := a.mcpIndexLocked(event.ID)
			if index >= 0 && !a.mcpServers[index].Disabled && mcphost.Key(mcphost.Definition{Config: a.mcpServers[index]}) == event.Key {
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
