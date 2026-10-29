package server

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"vista/internal/mcphost"
)

func TestMain(m *testing.M) {
	if os.Getenv("VISTA_MCP_HOST_PROCESS") == "1" {
		if err := mcphost.Run(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// Existing HTTP contracts test check results independently of pushed runtime
// availability. The real subprocess still supplies the shared connection.
func testMCPHost(t *testing.T, a *App) {
	t.Helper()
	if a.mcpHost != nil {
		return
	}
	parent := a.ctx
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	a.ctx = ctx
	a.dir = filepath.Join(a.configDir, "state")
	if err := os.MkdirAll(a.dir, 0700); err != nil {
		t.Fatal(err)
	}
	a.mcpHost = mcphost.NewProcess(mcphost.ProcessOptions{ConfigDir: a.configDir, StateDir: a.dir, Env: os.Environ(), Snapshot: func() ([]mcphost.Definition, int64) {
		return mcphost.Definitions(a.currentMCPServers(), nil), 0
	}})
	a.background.Go(func() { a.mcpHost.Run(ctx) })
	t.Cleanup(func() { cancel(); a.background.Wait() })
}
