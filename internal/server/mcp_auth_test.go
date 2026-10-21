package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
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

func mcpOAuthFixture(t *testing.T, hold <-chan struct{}, authenticated chan<- struct{}) string {
	t.Helper()
	var base string
	var once sync.Once
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return mcpSettingsFixture() }, &mcp.StreamableHTTPOptions{JSONResponse: true, Stateless: true})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/oauth-protected-resource/mcp":
			jsonOut(w, map[string]any{"resource": base + "/mcp", "authorization_servers": []string{base}})
		case "/.well-known/oauth-authorization-server":
			jsonOut(w, map[string]any{"issuer": base, "authorization_endpoint": base + "/authorize", "token_endpoint": base + "/token", "response_types_supported": []string{"code"}, "code_challenge_methods_supported": []string{"S256"}})
		case "/authorize":
			if r.URL.Query().Get("client_id") != "vista-test" || r.URL.Query().Get("code_challenge") == "" {
				http.Error(w, "invalid OAuth request", 400)
				return
			}
			redirect, err := url.Parse(r.URL.Query().Get("redirect_uri"))
			if err != nil {
				http.Error(w, "invalid redirect", 400)
				return
			}
			q := redirect.Query()
			q.Set("code", "test-code")
			q.Set("state", r.URL.Query().Get("state"))
			redirect.RawQuery = q.Encode()
			http.Redirect(w, r, redirect.String(), http.StatusFound)
		case "/token":
			jsonOut(w, map[string]any{"access_token": "oauth-private-token", "token_type": "Bearer", "expires_in": 3600, "refresh_token": "oauth-private-refresh"})
		case "/mcp":
			if r.Header.Get("Authorization") != "Bearer oauth-private-token" {
				w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+base+`/.well-known/oauth-protected-resource/mcp"`)
				http.Error(w, "unauthorized", 401)
				return
			}
			if hold != nil {
				once.Do(func() { close(authenticated) })
				select {
				case <-hold:
				case <-r.Context().Done():
					return
				}
			}
			handler.ServeHTTP(w, r)
		default:
			http.NotFound(w, r)
		}
	}))
	base = server.URL
	t.Cleanup(server.Close)
	return server.URL + "/mcp"
}

func awaitMCPLogin(t *testing.T, a *App, id string, matches func(mcpLogin) bool) mcpLogin {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		w := mcpAPI(t, a, "GET", "/mcp-auth/"+id, nil, 200)
		var login mcpLogin
		if err := json.Unmarshal(w.Body.Bytes(), &login); err != nil {
			t.Fatal(err)
		}
		if matches(login) {
			return login
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("OAuth login did not reach expected state")
	return mcpLogin{}
}

func startMCPAuth(t *testing.T, a *App, config workerproto.MCPServer) mcpLogin {
	t.Helper()
	mcpAPI(t, a, "POST", "/mcp-config", config, 204)
	w := mcpAPI(t, a, "POST", "/mcp-servers/"+config.ID+"/login", nil, 200)
	var login mcpLogin
	if err := json.Unmarshal(w.Body.Bytes(), &login); err != nil {
		t.Fatal(err)
	}
	return awaitMCPLogin(t, a, login.ID, func(value mcpLogin) bool { return value.Status == "pending" && value.AuthorizationURL != "" })
}

func TestMCPBrowserOAuthPublishesCredentialsAndChecksTools(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := &App{configDir: mcpConfigDir(t), ctx: ctx}
	config := workerproto.MCPServer{ID: "oauth", Name: "Original OAuth service", Transport: "http", Endpoint: mcpOAuthFixture(t, nil, nil), OAuth: true, ClientID: "vista-test"}
	login := startMCPAuth(t, a, config)
	if mcpclient.OAuthConfigured(a.configDir, config) {
		t.Fatal("authorization published before browser approval")
	}
	response, err := http.Get(login.AuthorizationURL)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	login = awaitMCPLogin(t, a, login.ID, func(value mcpLogin) bool { return !value.active() })
	if login.Status != "connected" || login.AuthorizationURL != "" || !mcpclient.OAuthConfigured(a.configDir, config) {
		t.Fatalf("login not committed: %+v", login)
	}
	config.Name = "已更名的服务"
	mcpAPI(t, a, "PUT", "/mcp-config", config, 204)
	loaded, err := loadMCPConfig(a.configDir)
	if err != nil || len(loaded) != 1 || loaded[0].ID != config.ID || loaded[0].Name != config.Name || !mcpclient.OAuthConfigured(a.configDir, loaded[0]) {
		t.Fatalf("renamed OAuth connection failed reload: %+v %v", loaded, err)
	}
	view := mcpAPI(t, a, "GET", "/mcp-config", nil, 200).Body.String()
	if !strings.Contains(view, `"authorized":true`) || strings.Contains(view, "oauth-private") {
		t.Fatalf("authorized settings: %s", view)
	}
	w := mcpAPI(t, a, "POST", "/mcp-servers/oauth/check", nil, 200)
	if !strings.Contains(w.Body.String(), `"status":"available"`) || !strings.Contains(w.Body.String(), `"toolCount":2`) {
		t.Fatalf("saved OAuth credentials unusable for check: %s", w.Body.String())
	}
	otherConfig := config
	otherConfig.ID, otherConfig.Name = "other-id", "other"
	other := startMCPAuth(t, a, otherConfig)
	// Another browser tab can authorize a different server without making an
	// already-completed result disappear before the original tab polls it.
	if got := mcpAPI(t, a, "GET", "/mcp-auth/"+login.ID, nil, 200).Body.String(); !strings.Contains(got, `"status":"connected"`) {
		t.Fatalf("starting another login lost the completed result: %s", got)
	}
	mcpAPI(t, a, "DELETE", "/mcp-auth/"+other.ID, nil, 200)
	config.ClientID = "other-client"
	mcpAPI(t, a, "PUT", "/mcp-config", config, 204)
	if _, err := os.Stat(filepath.Join(a.configDir, "mcp-oauth", "oauth.json")); !os.IsNotExist(err) {
		t.Fatalf("changed identity kept saved authorization: %v", err)
	}
}

func TestMCPBrowserOAuthRenameDuringAuthorizationPreservesLogin(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := &App{configDir: mcpConfigDir(t), ctx: ctx}
	hold, authenticated := make(chan struct{}), make(chan struct{})
	var release sync.Once
	unblock := func() { release.Do(func() { close(hold) }) }
	defer unblock()
	config := workerproto.MCPServer{ID: "stable-connection", Name: "Before login", Transport: "http", Endpoint: mcpOAuthFixture(t, hold, authenticated), OAuth: true, ClientID: "vista-test"}
	login := startMCPAuth(t, a, config)
	config.Name = "Waiting for browser"
	mcpAPI(t, a, "PUT", "/mcp-config", config, 204)
	view := awaitMCPLogin(t, a, login.ID, func(value mcpLogin) bool { return value.Name == config.Name })
	if view.Status != "pending" || view.ServerID != config.ID || view.AuthorizationURL != login.AuthorizationURL {
		t.Fatalf("rename interrupted pending login: %+v", view)
	}
	response, err := http.Get(login.AuthorizationURL)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	select {
	case <-authenticated:
	case <-time.After(5 * time.Second):
		t.Fatal("login did not reach authenticated handshake")
	}
	config.Name = "更名后的连接"
	mcpAPI(t, a, "PUT", "/mcp-config", config, 204)
	unblock()
	login = awaitMCPLogin(t, a, login.ID, func(value mcpLogin) bool { return !value.active() })
	if login.Status != "connected" || login.Name != config.Name || !mcpclient.OAuthConfigured(a.configDir, config) {
		t.Fatalf("rename discarded completed authorization: %+v", login)
	}
	checked := mcpAPI(t, a, "POST", "/mcp-servers/"+config.ID+"/check", nil, 200)
	if !strings.Contains(checked.Body.String(), `"status":"available"`) {
		t.Fatalf("renamed connection could not use its authorization: %s", checked.Body.String())
	}
}

func TestMCPBrowserOAuthCancelAndEditsNeverPublishLateTokens(t *testing.T) {
	for _, action := range []string{"cancel", "delete", "edit", "disable"} {
		t.Run(action, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			a := &App{configDir: mcpConfigDir(t), ctx: ctx}
			hold, authenticated := make(chan struct{}), make(chan struct{})
			config := workerproto.MCPServer{ID: "oauth", Name: "OAuth display name", Transport: "http", Endpoint: mcpOAuthFixture(t, hold, authenticated), OAuth: true, ClientID: "vista-test"}
			login := startMCPAuth(t, a, config)
			response, err := http.Get(login.AuthorizationURL)
			if err != nil {
				t.Fatal(err)
			}
			response.Body.Close()
			select {
			case <-authenticated:
			case <-time.After(5 * time.Second):
				t.Fatal("login did not finish its token exchange")
			}
			if mcpclient.OAuthConfigured(a.configDir, config) {
				t.Fatal("staged token leaked before the handshake completed")
			}
			switch action {
			case "cancel":
				mcpAPI(t, a, "DELETE", "/mcp-auth/"+login.ID, nil, 200)
			case "delete":
				mcpAPI(t, a, "DELETE", "/mcp-config", map[string]string{"id": config.ID}, 204)
			case "edit":
				config.ClientID = "new-client"
				mcpAPI(t, a, "PUT", "/mcp-config", config, 204)
			case "disable":
				mcpAPI(t, a, "PATCH", "/mcp-config", map[string]any{"id": config.ID, "disabled": true}, 204)
			}
			close(hold)
			login = awaitMCPLogin(t, a, login.ID, func(value mcpLogin) bool { return value.Status == "canceled" })
			if login.AuthorizationURL != "" {
				t.Fatal("canceled login kept its authorization link")
			}
			// Wait for the background flow and staging directory to be removed.
			deadline := time.Now().Add(5 * time.Second)
			for {
				entries, err := filepath.Glob(filepath.Join(a.configDir, ".mcp-login-*"))
				if err != nil {
					t.Fatal(err)
				}
				if len(entries) == 0 {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("canceled OAuth login leaked its staging directory")
				}
				time.Sleep(10 * time.Millisecond)
			}
			if _, err := os.Stat(filepath.Join(a.configDir, "mcp-oauth", "oauth.json")); !os.IsNotExist(err) {
				t.Fatalf("late authorization resurrected credentials: %v", err)
			}
		})
	}
}
