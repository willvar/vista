package mcpclient

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"vista/internal/network"
	"vista/internal/platform"

	"vista/internal/useragent"
)

func TestSearchPreservesExplicitNetworkIncludingDirect(t *testing.T) {
	current := network.Policy{Profile: &network.Profile{ID: "current", Name: "Current", Protocol: "http", Authentication: "none", Server: "proxy.invalid", Port: 8080}}
	for _, explicit := range []bool{false, true} {
		config := Config{ID: "search", Name: "search", Transport: "http", Endpoint: "https://search.invalid/mcp"}
		want := current
		if explicit {
			want = network.Policy{}
			config.Network = &want
		}
		called := false
		ctx := WithConnector(network.WithPolicy(context.Background(), current), func(_ context.Context, got Config, search bool) (Connection, error) {
			called = true
			if !search || got.Network == nil || network.Fingerprint(*got.Network) != network.Fingerprint(want) {
				t.Fatalf("explicit Direct=%v: search lost its network policy", explicit)
			}
			return nil, nil
		})
		if _, err := OpenSearch(ctx, config); err != nil || !called {
			t.Fatalf("explicit Direct=%v: %v", explicit, err)
		}
	}
}

type poolTrackedConn struct {
	net.Conn
	closed *atomic.Int32
	notify chan<- struct{}
	once   sync.Once
}

func (c *poolTrackedConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(func() {
		c.closed.Add(1)
		select {
		case c.notify <- struct{}{}:
		default:
		}
	})
	return err
}

func TestStreamableHTTPOwnedPoolClosesOnSuccessAndFailure(t *testing.T) {
	for _, mode := range []string{"success", "http-error", "protocol-mismatch"} {
		t.Run(mode, func(t *testing.T) {
			var opened, closed atomic.Int32
			closedNotify := make(chan struct{}, 1)
			previous := http.DefaultTransport
			base := previous.(*http.Transport).Clone()
			base.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
				conn, err := new(net.Dialer).DialContext(ctx, network, address)
				if err != nil {
					return nil, err
				}
				opened.Add(1)
				return &poolTrackedConn{Conn: conn, closed: &closed, notify: closedNotify}, nil
			}
			http.DefaultTransport = base
			t.Cleanup(func() {
				base.CloseIdleConnections()
				http.DefaultTransport = previous
			})
			handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return fixtureServer() }, &mcp.StreamableHTTPOptions{JSONResponse: true, Stateless: true})
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch mode {
				case "http-error":
					http.Error(w, "fixture unavailable", http.StatusBadGateway)
				case "protocol-mismatch":
					var request struct {
						ID     json.RawMessage `json:"id"`
						Method string          `json:"method"`
					}
					json.NewDecoder(r.Body).Decode(&request)
					if request.Method == "initialize" {
						w.Header().Set("Content-Type", "application/json")
						json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{"protocolVersion": "2025-03-26", "capabilities": map[string]any{}, "serverInfo": map[string]any{"name": "older-fixture", "version": "1"}}})
					} else {
						w.WriteHeader(http.StatusAccepted)
					}
				default:
					handler.ServeHTTP(w, r)
				}
			}))
			defer upstream.Close()
			connection, err := Open(context.Background(), Config{ID: "owned", Name: "Owned", Transport: "http", Endpoint: upstream.URL})
			if mode == "success" {
				if err != nil {
					t.Fatal(err)
				}
				if _, err := connection.ListTools(context.Background(), ""); err != nil {
					connection.Close()
					t.Fatal(err)
				}
				if err := connection.Close(); err != nil {
					t.Fatal(err)
				}
			} else if err == nil {
				connection.Close()
				t.Fatal("invalid initialization succeeded")
			}
			if opened.Load() == 0 {
				t.Fatal("fixture did not open an HTTP connection")
			}
			deadline := time.NewTimer(time.Second)
			defer deadline.Stop()
			for closed.Load() != opened.Load() {
				select {
				case <-closedNotify:
				case <-deadline.C:
					t.Fatalf("owned HTTP pool was not released: opened=%d closed=%d", opened.Load(), closed.Load())
				}
			}
		})
	}
}

func fixtureServer() *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "vista-fixture", Version: "1"}, nil)
	server.AddTool(&mcp.Tool{Name: "echo", Description: "Echo text", InputSchema: json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}`)}, func(_ context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var input struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal(request.Params.Arguments, &input); err != nil {
			return nil, err
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: input.Text}}}, nil
	})
	server.AddTool(&mcp.Tool{Name: "fail", Description: "Return a tool error", InputSchema: json.RawMessage(`{"type":"object"}`)}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "fixture failure"}}}, nil
	})
	server.AddTool(&mcp.Tool{Name: "wait", Description: "Wait for cancellation", InputSchema: json.RawMessage(`{"type":"object"}`)}, func(ctx context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Second):
			return nil, errors.New("fixture wait elapsed")
		}
	})
	server.AddTool(&mcp.Tool{Name: "large", Description: "Return a bounded large result", InputSchema: json.RawMessage(`{"type":"object"}`)}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: strings.Repeat("x", 16<<10)}}}, nil
	})
	server.AddTool(&mcp.Tool{Name: "huge", Description: "Return an oversized HTTP response", InputSchema: json.RawMessage(`{"type":"object"}`)}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: strings.Repeat("x", 300<<10)}}}, nil
	})
	return server
}

func TestMCPFixture(t *testing.T) {
	if os.Getenv("VISTA_MCP_FIXTURE") != "1" {
		return
	}
	if os.Getenv("VISTA_MCP_CHILD_PID") != "" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestMCPDescendantFixture$")
		if err := cmd.Start(); err != nil {
			os.Exit(2)
		}
	}
	if err := fixtureServer().Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

func TestMCPDescendantFixture(t *testing.T) {
	path := os.Getenv("VISTA_MCP_CHILD_PID")
	if path == "" {
		return
	}
	if err := os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
		os.Exit(2)
	}
	for {
		time.Sleep(time.Hour)
	}
}

func TestStdioTransportClosesDescendants(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "child.pid")
	t.Setenv("VISTA_MCP_FIXTURE", "1")
	t.Setenv("VISTA_MCP_CHILD_PID", path)
	server, err := Open(context.Background(), Config{ID: "local", Name: "local", Transport: "stdio", Command: os.Args[0], Args: []string{"-test.run=^TestMCPFixture$"}, Directory: root})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	var pid int
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		data, _ := os.ReadFile(path)
		pid, _ = strconv.Atoi(strings.TrimSpace(string(data)))
		if pid > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if pid == 0 {
		t.Fatal("MCP descendant did not start")
	}
	server.Close()
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !platform.IsAlive(pid) {
			return
		}
		if runtime.GOOS == "linux" {
			data, _ := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
			if strings.Contains(string(data), ") Z ") {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("MCP descendant %d survived Close", pid)
}

func checkServer(t *testing.T, config Config) {
	t.Helper()
	server, err := Open(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	page, err := server.ListTools(context.Background(), "")
	if err != nil || len(page.Tools) != 5 || page.NextCursor != "" {
		t.Fatalf("unexpected tools: %+v %v", page, err)
	}
	result, err := server.CallTool(context.Background(), "echo", json.RawMessage(`{"text":"hello"}`))
	if err != nil || result.IsError || result.Content != "hello" {
		t.Fatalf("unexpected call result: %+v %v", result, err)
	}
	result, err = server.CallTool(context.Background(), "fail", json.RawMessage(`{}`))
	if err != nil || !result.IsError || result.Content != "fixture failure" {
		t.Fatalf("MCP tool error was not preserved: %+v %v", result, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	stop := time.AfterFunc(100*time.Millisecond, cancel)
	defer stop.Stop()
	_, err = server.CallTool(ctx, "wait", json.RawMessage(`{}`))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("MCP tool call did not stop on caller cancellation: %v", err)
	}
	cancel()
	result, err = server.CallTool(context.Background(), "echo", json.RawMessage(`{"text":"after cancel"}`))
	if err != nil || result.IsError || result.Content != "after cancel" {
		t.Fatalf("MCP connection did not recover after cancellation: %+v %v", result, err)
	}
	if result, err := server.CallTool(context.Background(), "large", json.RawMessage(`{}`)); err != nil || result.Content != strings.Repeat("x", 16<<10) {
		t.Fatalf("successful MCP output was rejected before model previewing: %d bytes, error=%v", len(result.Content), err)
	}
	result, err = server.CallTool(context.Background(), "echo", json.RawMessage(`{"text":"after oversized result"}`))
	if err != nil || result.IsError || result.Content != "after oversized result" {
		t.Fatalf("MCP connection did not recover after oversized result: %+v %v", result, err)
	}
}

func TestStdioTransport(t *testing.T) {
	t.Setenv("VISTA_MCP_FIXTURE", "1")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	checkServer(t, Config{ID: "local", Name: "local", Transport: "stdio", Command: exe, Args: []string{"-test.run=^TestMCPFixture$"}, Directory: t.TempDir()})
}

func TestStreamableHTTPIPv6OnlyLocalhost(t *testing.T) {
	listener, err := net.Listen("tcp6", "[::1]:0")
	if err != nil {
		t.Fatal(err)
	}
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return fixtureServer() }, &mcp.StreamableHTTPOptions{JSONResponse: true, Stateless: true})
	server := &http.Server{Handler: handler}
	go server.Serve(listener)
	defer server.Close()
	policy := network.Policy{}
	config := Config{ID: "ipv6", Name: "IPv6", Transport: "http", Endpoint: "http://localhost:" + strconv.Itoa(listener.Addr().(*net.TCPAddr).Port), Network: &policy}
	connection, err := Open(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	result, err := connection.CallTool(context.Background(), "echo", json.RawMessage(`{"text":"IPv6 MCP"}`))
	connection.Close()
	if err != nil || result.Content != "IPv6 MCP" {
		t.Fatalf("IPv6 HTTP MCP : %+v %v", result, err)
	}
}

func TestStreamableHTTPTransportWithBearer(t *testing.T) {
	wantAgent, agentErr := useragent.Current()
	if agentErr != nil {
		t.Fatal(agentErr)
	}
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return fixtureServer() }, &mcp.StreamableHTTPOptions{JSONResponse: true, Stateless: true})
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {

		if r.UserAgent() != wantAgent {
			t.Errorf("sent UA %q, want %q", r.UserAgent(), wantAgent)
		}
		if r.Header.Get("Authorization") != "Bearer test-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.Header.Get("CONTEXT7_API_KEY") != "context-secret" {
			http.Error(w, "missing custom authentication header", http.StatusUnauthorized)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	defer httpServer.Close()
	config := Config{ID: "remote", Name: "remote", Transport: "http", Endpoint: httpServer.URL, Token: "test-token", Headers: map[string]string{"CONTEXT7_API_KEY": "context-secret"}}
	checkServer(t, config)
	connection, err := Open(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if _, err := connection.CallTool(context.Background(), "huge", json.RawMessage(`{}`)); err == nil || !strings.Contains(err.Error(), "MCP HTTP response exceeds 256 KiB") {
		t.Fatalf("oversized MCP HTTP response was accepted: %v", err)
	}
	if _, err := Open(context.Background(), Config{ID: "bad", Name: "bad", Transport: "http", Endpoint: "http://example.com", Token: "secret"}); err == nil {
		t.Fatal("unsafe token endpoint was accepted")
	}
	if _, err := Open(context.Background(), Config{ID: "bad", Name: "bad", Transport: "stdio", Command: "relative", Directory: filepath.Join(t.TempDir(), "x")}); err == nil {
		t.Fatal("relative stdio command was accepted")
	}
}

func TestMCPRequestHeadersRejectAmbiguousAuthenticationAndProtocolOverrides(t *testing.T) {
	for name, config := range map[string]Config{
		"duplicate":      {Headers: map[string]string{"X-Key": "one", "x-key": "two"}},
		"newline":        {Headers: map[string]string{"X-Key": "one\r\ntwo"}},
		"invalid name":   {Headers: map[string]string{"X Key": "one"}},
		"token conflict": {Token: "token", Headers: map[string]string{"Authorization": "Basic test"}},
		"oauth conflict": {OAuth: true, Headers: map[string]string{"Authorization": "Basic test"}},
		"protocol":       {Headers: map[string]string{"MCP-Protocol-Version": "wrong"}},
		"session":        {Headers: map[string]string{"MCP-Session-Id": "wrong"}},
		"accept":         {Headers: map[string]string{"Accept": "wrong"}},
		"framing":        {Headers: map[string]string{"Content-Length": "0"}},
	} {
		t.Run(name, func(t *testing.T) {
			config.ID, config.Name, config.Transport, config.Endpoint = "test", "test", "http", "https://example.invalid/mcp"
			if ValidateConfig(config) == nil {
				t.Fatal("invalid headers accepted")
			}
		})
	}
}
