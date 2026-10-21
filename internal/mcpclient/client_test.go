package mcpclient

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"vista/internal/platform"

	"vista/internal/useragent"
)

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
		handler.ServeHTTP(w, r)
	}))
	defer httpServer.Close()
	checkServer(t, Config{ID: "remote", Name: "remote", Transport: "http", Endpoint: httpServer.URL, Token: "test-token"})
	connection, err := Open(context.Background(), Config{ID: "remote", Name: "remote", Transport: "http", Endpoint: httpServer.URL, Token: "test-token"})
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
