package mcphost

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"vista/internal/workerproto"
)

func TestHostedHTTPToolsAndSearchReuseHandshakesAndTCP(t *testing.T) {
	var connections atomic.Int32
	var mu sync.Mutex
	methods := map[string]int{}
	server := mcp.NewServer(&mcp.Implementation{Name: "wire-fixture", Version: "1"}, nil)
	server.AddTool(&mcp.Tool{Name: "echo", InputSchema: json.RawMessage(`{"type":"object"}`)}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "fixture"}}}, nil
	})
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	upstream := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var call struct{ Method string }
		_ = json.Unmarshal(body, &call)
		mu.Lock()
		methods[call.Method]++
		mu.Unlock()
		r.Body = io.NopCloser(strings.NewReader(string(body)))
		handler.ServeHTTP(w, r)
	}))
	upstream.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			connections.Add(1)
		}
	}
	upstream.Start()
	defer upstream.Close()
	pool, ctx, host, stateDir := testPool(t)
	defs := Definitions([]workerproto.MCPServer{{ID: "custom", Name: "Custom", Transport: "http", Endpoint: upstream.URL + "/custom"}}, []workerproto.SearchProvider{
		{Name: "exa", Endpoint: upstream.URL + "/exa"},
		{Name: "firecrawl", Endpoint: upstream.URL + "/firecrawl"},
		{Name: "parallel", Endpoint: upstream.URL + "/parallel"},
	})
	if err := pool.Sync(1, defs); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		lease, err := Acquire(ctx, host, stateDir, defs)
		if err != nil {
			t.Fatal(err)
		}
		for _, def := range defs {
			proxy, err := lease.Connect(ctx, def.Config, def.Search)
			if err == nil {
				_, err = proxy.CallTool(ctx, "echo", json.RawMessage(`{}`))
			}
			if err != nil {
				t.Fatal(err)
			}
		}
		_ = lease.Close()
	}
	mu.Lock()
	valid := methods["initialize"] == 4 && methods["notifications/initialized"] == 4 && methods["tools/call"] == 8
	mu.Unlock()
	if !valid {
		t.Fatalf("unexpected wire requests: %v", methods)
	}
	if connections.Load() != 4 {
		t.Fatalf("upstream TCP connections=%d; expected one per configured version", connections.Load())
	}
	pool.Close()
}

func TestHostedHTTPExpiredSessionReconnectsWithoutReplayingCall(t *testing.T) {
	var initialized, toolRequests, available atomic.Int32
	var expired atomic.Bool
	var oldSession atomic.Value
	oldSession.Store("")
	server := mcp.NewServer(&mcp.Implementation{Name: "stateful-fixture", Version: "1"}, nil)
	server.AddTool(&mcp.Tool{Name: "echo", InputSchema: json.RawMessage(`{"type":"object"}`)}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "fixture"}}}, nil
	})
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{JSONResponse: true})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var call struct{ Method string }
		_ = json.Unmarshal(body, &call)
		switch call.Method {
		case "initialize":
			initialized.Add(1)
		case "tools/call":
			toolRequests.Add(1)
			if oldSession.Load().(string) == "" {
				oldSession.Store(r.Header.Get("Mcp-Session-Id"))
			}
		}
		if expired.Load() && r.Header.Get("Mcp-Session-Id") == oldSession.Load().(string) {
			http.Error(w, "session not found", http.StatusNotFound)
			return
		}
		r.Body = io.NopCloser(strings.NewReader(string(body)))
		handler.ServeHTTP(w, r)
	}))
	defer upstream.Close()
	pool, ctx, host, stateDir := testPool(t)
	pool.notify = func(event Event) {
		if event.State.Status == "available" {
			available.Add(1)
		}
	}
	def := definition("stateful")
	def.Config.Endpoint = upstream.URL
	if err := pool.Sync(1, []Definition{def}); err != nil {
		t.Fatal(err)
	}
	lease, err := Acquire(ctx, host, stateDir, []Definition{def})
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	proxy, err := lease.Connect(ctx, def.Config, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := proxy.CallTool(ctx, "echo", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	if oldSession.Load().(string) == "" {
		t.Fatal("fixture did not create a stateful session")
	}
	eventually(t, func() bool { return available.Load() >= 1 })
	before := available.Load()
	expired.Store(true)
	if _, err := proxy.CallTool(ctx, "echo", json.RawMessage(`{}`)); err == nil {
		t.Fatal("expired session reported success")
	}
	eventually(t, func() bool { return available.Load() > before })
	if initialized.Load() != 2 || toolRequests.Load() != 2 {
		t.Fatalf("reconnection replayed a call: initialize=%d tools/call=%d", initialized.Load(), toolRequests.Load())
	}
	result, err := proxy.CallTool(ctx, "echo", json.RawMessage(`{}`))
	if err != nil || result.Content != "fixture" || toolRequests.Load() != 3 {
		t.Fatalf("lease did not use the new session: %+v %v", result, err)
	}
}
