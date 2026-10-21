package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"vista/internal/mcpclient"
	"vista/internal/workerproto"
)

func mcpAPI(t *testing.T, a *App, method, path string, body any, status int) *httptest.ResponseRecorder {
	t.Helper()
	var data []byte
	if body != nil {
		var err error
		data, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	r := httptest.NewRequest(method, "http://127.0.0.1:5186/api"+path, strings.NewReader(string(data)))
	w := httptest.NewRecorder()
	a.handler().ServeHTTP(w, r)
	if w.Code != status {
		t.Fatalf("%s %s: status=%d, want=%d, body=%s", method, path, w.Code, status, w.Body.String())
	}
	return w
}

func mcpConfigDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestMCPSettingsCRUDAndExecutionSnapshots(t *testing.T) {
	a := &App{configDir: mcpConfigDir(t)}
	config := workerproto.MCPServer{ID: "remote", Name: "remote", Transport: "http", Endpoint: "https://mcp.example/tools", Token: "private-token"}
	mcpAPI(t, a, "POST", "/mcp-config", config, 204)
	before := a.currentMCPServers()
	if len(before) != 1 || before[0].Token != config.Token {
		t.Fatalf("next execution did not receive saved config: %+v", before)
	}
	mcpAPI(t, a, "POST", "/mcp-config", config, 409)
	config.Token = ""
	config.Endpoint = "https://mcp.example/updated"
	mcpAPI(t, a, "PUT", "/mcp-config", config, 204)
	if got := a.currentMCPServers(); got[0].Token != "private-token" || got[0].Endpoint != config.Endpoint || before[0].Endpoint == config.Endpoint {
		t.Fatalf("blank token or previous snapshot changed: %+v / %+v", got, before)
	}
	view := mcpAPI(t, a, "GET", "/mcp-config", nil, 200).Body.String()
	if strings.Contains(view, "private-token") || strings.Contains(view, `"token"`) || !strings.Contains(view, `"hasToken":true`) {
		t.Fatalf("credential readback: %s", view)
	}
	mcpAPI(t, a, "PATCH", "/mcp-config", map[string]any{"id": config.ID, "disabled": true}, 204)
	if len(a.currentMCPServers()) != 0 {
		t.Fatal("disabled service included in next execution")
	}
	loaded, err := loadMCPConfig(a.configDir)
	if err != nil || len(loaded) != 1 || !loaded[0].Disabled {
		t.Fatalf("saved settings failed to reload: %+v %v", loaded, err)
	}
	info, err := os.Stat(filepath.Join(a.configDir, "mcp.json"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("config permissions: %v %v", info, err)
	}
	mcpAPI(t, a, "PUT", "/mcp-config", map[string]any{"id": config.ID, "name": "remote", "transport": "http", "endpoint": config.Endpoint, "clearToken": true}, 204)
	if got := a.currentMCPServers(); len(got) != 1 || got[0].Token != "" {
		t.Fatalf("explicit token removal failed: %+v", got)
	}
	config.OAuth, config.ClientID, config.ClientSecret = true, "client", "private-client-secret"
	mcpAPI(t, a, "PUT", "/mcp-config", config, 204)
	config.ClientSecret = ""
	mcpAPI(t, a, "PUT", "/mcp-config", config, 204)
	got := a.currentMCPServers()[0]
	if got.ClientSecret != "private-client-secret" || got.Token != "" {
		t.Fatalf("OAuth secret preservation/auth transition: %+v", got)
	}
	view = mcpAPI(t, a, "GET", "/mcp-config", nil, 200).Body.String()
	if strings.Contains(view, "private-client-secret") || strings.Contains(view, `"clientSecret"`) || !strings.Contains(view, `"hasClientSecret":true`) {
		t.Fatalf("OAuth secret exposed: %s", view)
	}
	mcpAPI(t, a, "PUT", "/mcp-config", map[string]any{"id": config.ID, "name": config.Name, "transport": "http", "endpoint": config.Endpoint, "oauth": true, "clientId": "client", "clearClientSecret": true}, 204)
	if a.currentMCPServers()[0].ClientSecret != "" {
		t.Fatal("explicit client secret removal failed")
	}
	mcpAPI(t, a, "DELETE", "/mcp-config", map[string]string{"id": config.ID}, 204)
	if got, err := loadMCPConfig(a.configDir); err != nil || len(got) != 0 || len(a.currentMCPServers()) != 0 {
		t.Fatalf("deleted config reappeared: %+v %v", got, err)
	}
	mcpAPI(t, a, "DELETE", "/mcp-config", map[string]string{"id": config.ID}, 404)
}

func TestMCPSettingsRenamePreservesIDCredentialsChecksAndRunSnapshot(t *testing.T) {
	a := &App{configDir: mcpConfigDir(t)}
	mcpAPI(t, a, "POST", "/mcp-config", workerproto.MCPServer{Name: "Original name", Transport: "http", Endpoint: "https://mcp.example/tools", Token: "private-token"}, 204)
	before := a.currentMCPServers()
	config := before[0]
	if config.ID == "" || config.ID == config.Name {
		t.Fatalf("server has no independent ID: %+v", config)
	}
	checked := mcpCheck{Status: "available", ToolCount: 2, CheckedAt: time.Now().UTC()}
	a.mcpChecks = map[string]mcpCheck{config.ID: checked}
	config.Name, config.Token = "工作区工具", ""
	mcpAPI(t, a, "PUT", "/mcp-config", config, 204)
	got := a.currentMCPServers()[0]
	if got.ID != config.ID || got.Name != config.Name || got.Token != "private-token" || a.mcpChecks[config.ID] != checked {
		t.Fatalf("rename changed connection: %+v / %+v", got, a.mcpChecks)
	}
	if before[0].Name != "Original name" || before[0].ID != got.ID {
		t.Fatalf("rename changed running execution snapshot: %+v", before)
	}
	loaded, err := loadMCPConfig(a.configDir)
	if err != nil || len(loaded) != 1 || !sameMCPConnection(loaded[0], got) || loaded[0].Name != got.Name {
		t.Fatalf("rename did not survive reload: %+v %v", loaded, err)
	}
	// Neither an existing ID nor another connection's display name can be taken.
	other := workerproto.MCPServer{ID: "other-id", Name: "Other name", Transport: "http", Endpoint: config.Endpoint}
	mcpAPI(t, a, "POST", "/mcp-config", other, 204)
	other.ID = config.ID
	mcpAPI(t, a, "POST", "/mcp-config", other, 409)
	other.ID, other.Name = "other-id", config.Name
	mcpAPI(t, a, "PUT", "/mcp-config", other, 409)
	if a.currentMCPServers()[1].Name != "Other name" {
		t.Fatal("rejected rename changed the saved name")
	}
	mcpAPI(t, a, "PATCH", "/mcp-config", map[string]any{"name": config.Name, "disabled": true}, 400)
	mcpAPI(t, a, "PATCH", "/mcp-config", map[string]any{"id": config.ID, "disabled": true}, 204)
	mcpAPI(t, a, "DELETE", "/mcp-config", map[string]string{"id": config.ID}, 204)
	if len(a.currentMCPServers()) != 1 || a.currentMCPServers()[0].ID != "other-id" {
		t.Fatal("rename broke ID-based toggle or deletion")
	}
}

func TestMCPSettingsRejectInvalidAndFailedWrites(t *testing.T) {
	a := &App{configDir: mcpConfigDir(t)}
	for _, config := range []workerproto.MCPServer{
		{ID: "invalid-name", Name: "bad\nname", Transport: "http", Endpoint: "https://example.com/mcp"},
		{Name: "unsafe", Transport: "http", Endpoint: "http://example.com/mcp"},
		{Name: "relative", Transport: "stdio", Command: "npx", Directory: "/tmp"},
	} {
		mcpAPI(t, a, "POST", "/mcp-config", config, 400)
	}
	for i := 0; i < 4; i++ {
		mcpAPI(t, a, "POST", "/mcp-config", workerproto.MCPServer{ID: fmt.Sprintf("test%d", i), Name: fmt.Sprintf("test%d", i), Transport: "http", Endpoint: "https://example.com/mcp"}, 204)
	}
	mcpAPI(t, a, "POST", "/mcp-config", workerproto.MCPServer{ID: "fifth", Name: "fifth", Transport: "http", Endpoint: "https://example.com/mcp"}, 400)
	if err := os.Chmod(a.configDir, 0755); err != nil {
		t.Fatal(err)
	}
	mcpAPI(t, a, "PATCH", "/mcp-config", map[string]any{"id": "test0", "disabled": true}, 500)
	if len(a.currentMCPServers()) != 4 {
		t.Fatal("failed persistence changed live execution config")
	}
}

func mcpSettingsFixture() *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "settings-fixture", Version: "1"}, &mcp.ServerOptions{PageSize: 1})
	for _, name := range []string{"echo", "last"} {
		server.AddTool(&mcp.Tool{Name: name, Description: "Settings test tool", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{}, nil
		})
	}
	return server
}

func TestMCPSettingsConnectionCheckListsAllPagesAndReportsFailure(t *testing.T) {
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return mcpSettingsFixture() }, &mcp.StreamableHTTPOptions{JSONResponse: true, Stateless: true})
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer check-secret" {
			http.Error(w, "invalid token", 401)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	defer remote.Close()
	a := &App{configDir: mcpConfigDir(t)}
	config := workerproto.MCPServer{ID: "check", Name: "check", Transport: "http", Endpoint: remote.URL, Token: "check-secret"}
	mcpAPI(t, a, "POST", "/mcp-config", config, 204)
	response := mcpAPI(t, a, "POST", "/mcp-servers/check/check", nil, 200)
	var result struct {
		Check mcpCheck         `json:"check"`
		Tools []mcpclient.Tool `json:"tools"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || result.Check.Status != "available" || result.Check.ToolCount != 2 || len(result.Tools) != 2 || result.Tools[1].Name != "last" {
		t.Fatalf("paginated check: %+v %v", result, err)
	}
	if got := mcpAPI(t, a, "GET", "/mcp-config", nil, 200).Body.String(); !strings.Contains(got, `"status":"available"`) {
		t.Fatalf("check status was not cached: %s", got)
	}
	config.Token = "wrong-secret"
	mcpAPI(t, a, "PUT", "/mcp-config", config, 204)
	if got := mcpAPI(t, a, "GET", "/mcp-config", nil, 200).Body.String(); !strings.Contains(got, `"status":"unchecked"`) {
		t.Fatalf("edit did not invalidate check: %s", got)
	}
	response = mcpAPI(t, a, "POST", "/mcp-servers/check/check", nil, 200)
	if !strings.Contains(response.Body.String(), `"status":"error"`) || strings.Contains(response.Body.String(), "wrong-secret") {
		t.Fatalf("failed check: %s", response.Body.String())
	}
}

func TestMCPSettingsConcurrentEditDiscardsStaleCheck(t *testing.T) {
	for _, rename := range []bool{false, true} {
		t.Run(fmt.Sprintf("rename=%t", rename), func(t *testing.T) {
			started, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return mcpSettingsFixture() }, &mcp.StreamableHTTPOptions{JSONResponse: true, Stateless: true})
			remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				once.Do(func() { close(started) })
				select {
				case <-release:
				case <-r.Context().Done():
					return
				}
				handler.ServeHTTP(w, r)
			}))
			defer remote.Close()
			a := &App{configDir: mcpConfigDir(t)}
			config := workerproto.MCPServer{ID: "check", Name: "Original display name", Transport: "http", Endpoint: remote.URL}
			mcpAPI(t, a, "POST", "/mcp-config", config, 204)
			request := httptest.NewRequest("POST", "http://127.0.0.1:5186/api/mcp-servers/check/check", nil)
			response := httptest.NewRecorder()
			done := make(chan struct{})
			go func() { defer close(done); a.handler().ServeHTTP(response, request) }()
			select {
			case <-started:
			case <-time.After(5 * time.Second):
				t.Fatal("check did not start")
			}
			if rename {
				config.Name = "新名称"
			} else {
				config.Token = "updated"
			}
			mcpAPI(t, a, "PUT", "/mcp-config", config, 204)
			close(release)
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("check did not finish")
			}
			if rename {
				if response.Code != 200 || a.mcpChecks[config.ID].Status != "available" {
					t.Fatalf("rename discarded valid check: %d %s %+v", response.Code, response.Body.String(), a.mcpChecks)
				}
			} else if response.Code != 409 || len(a.mcpChecks) != 0 {
				t.Fatalf("stale check survived config edit: %d %s %+v", response.Code, response.Body.String(), a.mcpChecks)
			}
		})
	}
}

func TestMCPSettingsSnapshotsDoNotAliasArgs(t *testing.T) {
	a := &App{mcpServers: []workerproto.MCPServer{{ID: "local", Name: "local", Args: []string{"original"}}}}
	got := a.currentMCPServers()
	got[0].Args[0] = "changed"
	if a.currentMCPServers()[0].Args[0] != "original" {
		t.Fatal("execution mutated saved MCP arguments")
	}
}

func TestMCPSettingsClosingCheckCancelsNetworkAndKeepsPreviousStatus(t *testing.T) {
	started, disconnected := make(chan struct{}), make(chan struct{})
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		close(started)
		<-r.Context().Done()
		close(disconnected)
	}))
	defer remote.Close()
	a := &App{configDir: mcpConfigDir(t)}
	mcpAPI(t, a, "POST", "/mcp-config", workerproto.MCPServer{ID: "cancel", Name: "cancel", Transport: "http", Endpoint: remote.URL}, 204)
	a.mcpChecks = map[string]mcpCheck{"cancel": {Status: "available", ToolCount: 2}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := httptest.NewRequest("POST", "http://127.0.0.1:5186/api/mcp-servers/cancel/check", nil).WithContext(ctx)
	done := make(chan struct{})
	go func() { defer close(done); a.handler().ServeHTTP(httptest.NewRecorder(), r) }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("check did not open a connection")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("closed check did not stop")
	}
	select {
	case <-disconnected:
	case <-time.After(5 * time.Second):
		t.Fatal("closed check kept its network request alive")
	}
	if a.mcpChecks["cancel"].Status != "available" {
		t.Fatalf("cancellation replaced the previous availability check: %+v", a.mcpChecks)
	}
}
