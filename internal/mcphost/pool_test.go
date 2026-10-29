package mcphost

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"vista/internal/mcpclient"
	"vista/internal/workerproto"
)

type fixtureConnection struct {
	closed atomic.Bool
	calls  atomic.Int32
}

func (c *fixtureConnection) ListTools(context.Context, string) (mcpclient.ToolPage, error) {
	return mcpclient.ToolPage{Tools: []mcpclient.Tool{{Name: "echo", InputSchema: map[string]any{"type": "object"}}}}, nil
}

func (c *fixtureConnection) CallTool(ctx context.Context, name string, _ json.RawMessage) (mcpclient.CallResult, error) {
	c.calls.Add(1)
	if c.closed.Load() {
		return mcpclient.CallResult{}, errors.New("connection closed")
	}
	if name == "wait" {
		<-ctx.Done()
		return mcpclient.CallResult{}, ctx.Err()
	}
	return mcpclient.CallResult{Content: strings.Repeat("字", 30<<10)}, nil
}

func (c *fixtureConnection) Close() error { c.closed.Store(true); return nil }

func definition(id string) Definition {
	return Definition{Config: mcpclient.Config{ID: id, Name: id, Transport: "http", Endpoint: "https://example.invalid/" + id}}
}

func eventually(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition did not become true")
}

func testPool(t *testing.T) (*Pool, context.Context, workerproto.MCPHost, string) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	stateDir := t.TempDir()
	root := filepath.Join(stateDir, "mcp-host-data")
	directory := filepath.Join(root, "test")
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	pool := NewPool(ctx, t.TempDir(), directory, nil)
	handler := &hostHandler{pool: pool, token: strings.Repeat("a", 64), root: root, leases: map[string]*hostedLease{}}
	server := httptest.NewServer(handler)
	t.Cleanup(func() { cancel(); pool.Close(); server.Close() })
	return pool, ctx, workerproto.MCPHost{Endpoint: server.URL, Token: handler.token}, stateDir
}

func TestPoolVersionsRenameDisableAndRelease(t *testing.T) {
	pool, ctx, _, _ := testPool(t)
	var opens atomic.Int32
	pool.connect = func(context.Context, Definition, string) (mcpclient.Connection, error) {
		opens.Add(1)
		return &fixtureConnection{}, nil
	}
	old := definition("one")
	if err := pool.Sync(1, []Definition{old}); err != nil {
		t.Fatal(err)
	}
	entries, release, err := pool.retain([]Definition{old})
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	server, err := pool.get(ctx, entries[Key(old)], false)
	if err != nil {
		t.Fatal(err)
	}
	old.Config.Name = "New display label"
	if err := pool.Sync(2, []Definition{old}); err != nil || opens.Load() != 1 {
		t.Fatalf("rename reconnected: opens=%d err=%v", opens.Load(), err)
	}
	updated := old
	updated.Config.Token = "updated-key"
	if err := pool.Sync(3, []Definition{updated}); err != nil {
		t.Fatal(err)
	}
	newEntries, newRelease, err := pool.retain([]Definition{updated})
	if err != nil {
		t.Fatal(err)
	}
	defer newRelease()
	newServer, err := pool.get(ctx, newEntries[Key(updated)], false)
	if err != nil || newServer == server || server.(*fixtureConnection).closed.Load() {
		t.Fatalf("edit destroyed old lease: %v", err)
	}
	if err := pool.Sync(4, nil); err != nil {
		t.Fatal(err)
	}
	if server.(*fixtureConnection).closed.Load() || newServer.(*fixtureConnection).closed.Load() {
		t.Fatal("disable interrupted active executions")
	}
	release()
	eventually(t, server.(*fixtureConnection).closed.Load)
	if newServer.(*fixtureConnection).closed.Load() {
		t.Fatal("releasing an old version closed the new one")
	}
	newRelease()
	eventually(t, newServer.(*fixtureConnection).closed.Load)
}

func TestPoolPreconnectIsBoundedAndSingleflightAcrossExecutions(t *testing.T) {
	pool, ctx, _, _ := testPool(t)
	var connecting, maximum, opens atomic.Int32
	gate := make(chan struct{})
	pool.connect = func(ctx context.Context, _ Definition, _ string) (mcpclient.Connection, error) {
		opens.Add(1)
		active := connecting.Add(1)
		defer connecting.Add(-1)
		for current := maximum.Load(); active > current && !maximum.CompareAndSwap(current, active); current = maximum.Load() {
		}
		select {
		case <-gate:
			return &fixtureConnection{}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	definitions := []Definition{definition("a"), definition("b"), definition("c"), definition("d")}
	if err := pool.Sync(1, definitions); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 10 {
		wg.Go(func() {
			entries, release, err := pool.retain(definitions)
			if err != nil {
				t.Error(err)
				return
			}
			defer release()
			if _, err := pool.get(ctx, entries[Key(definitions[0])], false); err != nil {
				t.Error(err)
			}
		})
	}
	eventually(t, func() bool { return connecting.Load() == 3 })
	close(gate)
	wg.Wait()
	eventually(t, func() bool { return opens.Load() == 4 })
	if maximum.Load() != 3 {
		t.Fatalf("handshake concurrency=%d", maximum.Load())
	}
}

func TestPoolSnapshotFailurePreservesCurrentAndReleasesPreparedEntries(t *testing.T) {
	pool, ctx, _, _ := testPool(t)
	pool.connect = func(context.Context, Definition, string) (mcpclient.Connection, error) {
		return &fixtureConnection{}, nil
	}
	current := definition("current")
	if err := pool.Sync(1, []Definition{current}); err != nil {
		t.Fatal(err)
	}
	entries, release, err := pool.retain([]Definition{current})
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	connection, err := pool.get(ctx, entries[Key(current)], false)
	if err != nil {
		t.Fatal(err)
	}
	authorized := definition("oauth")
	authorized.Config.OAuth = true
	if err := os.MkdirAll(filepath.Join(pool.configDir, "mcp-oauth"), 0700); err != nil {
		t.Fatal(err)
	}
	credential := `{"endpoint":"https://example.invalid/oauth","config":{"ClientID":"client","Endpoint":{"TokenURL":"https://example.invalid/token"}},"token":{"access_token":"private-token"}}`
	if err := os.WriteFile(filepath.Join(pool.configDir, "mcp-oauth", "oauth.json"), []byte(credential), 0600); err != nil {
		t.Fatal(err)
	}
	// Simulate storage failure while copying authorization, after a preceding
	// non-OAuth definition has already been prepared successfully.
	if err := os.WriteFile(filepath.Join(pool.directory, "auth"), []byte("blocked"), 0600); err != nil {
		t.Fatal(err)
	}
	definitions := []Definition{definition("new"), authorized}
	if err := pool.Sync(2, definitions); err == nil {
		t.Fatal("configuration update succeeded without its authorization snapshot")
	}
	if _, release, err := pool.retain(definitions); err == nil {
		release()
		t.Fatal("execution acquired an incomplete authorization snapshot")
	}
	pool.mu.Lock()
	unchanged := pool.revision == 1 && len(pool.entries) == 1 && entries[Key(current)].current
	pool.mu.Unlock()
	if !unchanged || connection.(*fixtureConnection).closed.Load() {
		t.Fatal("failed snapshot changed a current connection or retained incomplete entries")
	}
}

func TestHostCancellationLargeResultsAndLeaseEOF(t *testing.T) {
	pool, ctx, host, stateDir := testPool(t)
	connection := &fixtureConnection{}
	pool.connect = func(context.Context, Definition, string) (mcpclient.Connection, error) { return connection, nil }
	def := definition("shared")
	if err := pool.Sync(1, []Definition{def}); err != nil {
		t.Fatal(err)
	}
	one, err := Acquire(ctx, host, stateDir, []Definition{def})
	if err != nil {
		t.Fatal(err)
	}
	defer one.Close()
	two, err := Acquire(ctx, host, stateDir, []Definition{def})
	if err != nil {
		t.Fatal(err)
	}
	defer two.Close()
	proxy, _ := one.Connect(ctx, def.Config, false)
	requestCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { _, err := proxy.CallTool(requestCtx, "wait", json.RawMessage(`{}`)); done <- err }()
	eventually(t, func() bool { return connection.calls.Load() == 1 })
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("canceled call succeeded")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("canceled tool did not finish")
	}
	other, _ := two.Connect(ctx, def.Config, false)
	result, err := other.CallTool(ctx, "echo", json.RawMessage(`{}`))
	if err != nil || len(result.Content) != 3*30<<10 || connection.closed.Load() {
		t.Fatalf("other execution lost shared connection or result: %v", err)
	}
	var resultFiles []string
	_ = filepath.WalkDir(filepath.Join(stateDir, "mcp-host-data"), func(path string, entry os.DirEntry, err error) error {
		if err == nil && strings.HasPrefix(entry.Name(), "result-") {
			resultFiles = append(resultFiles, path)
		}
		return err
	})
	if len(resultFiles) != 0 {
		t.Fatalf("consumed results were retained: %v", resultFiles)
	}
	if err := pool.Sync(2, nil); err != nil {
		t.Fatal(err)
	}
	_ = one.Close()
	if connection.closed.Load() {
		t.Fatal("one execution's EOF closed another's connection")
	}
	_ = two.Close()
	eventually(t, connection.closed.Load)
}

func TestHostSearchAndConfiguredToolsUseTheSameConnector(t *testing.T) {
	pool, ctx, host, stateDir := testPool(t)
	var opens atomic.Int32
	pool.connect = func(context.Context, Definition, string) (mcpclient.Connection, error) {
		opens.Add(1)
		return &fixtureConnection{}, nil
	}
	definitions := Definitions([]workerproto.MCPServer{definition("custom").Config}, []workerproto.SearchProvider{
		{Name: "exa", Endpoint: "https://example.invalid/exa", APIKey: "private-exa-key"},
		{Name: "firecrawl", Endpoint: "https://example.invalid/firecrawl"},
		{Name: "parallel", Endpoint: "https://example.invalid/parallel"},
		{Name: "tavily", Endpoint: "https://example.invalid/tavily"},
	})
	if len(definitions) != 4 {
		t.Fatal("MCP search definitions missing or ordinary HTTP treated as MCP")
	}
	if err := pool.Sync(1, definitions); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		lease, err := Acquire(ctx, host, stateDir, definitions)
		if err != nil {
			t.Fatal(err)
		}
		for _, def := range definitions {
			var server *mcpclient.Server
			if def.Search {
				server, err = mcpclient.OpenSearch(lease.Context(ctx), def.Config.Endpoint, def.Config.Token)
			} else {
				server, err = mcpclient.Open(lease.Context(ctx), def.Config)
			}
			if err == nil {
				_, err = server.CallTool(ctx, "echo", json.RawMessage(`{}`))
				_ = server.Close()
			}
			if err != nil {
				t.Fatal(err)
			}
		}
		_ = lease.Close()
	}
	if opens.Load() != 4 {
		t.Fatalf("cross-execution connections opened %d times", opens.Load())
	}
}
