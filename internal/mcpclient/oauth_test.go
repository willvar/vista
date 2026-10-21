package mcpclient

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"vista/internal/appdirs"
)

func TestOAuthLoginPersistsAndRefreshesToken(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	configDir, err := appdirs.Config()
	if err != nil {
		t.Fatal(err)
	}
	var base string
	var challenge string
	var refreshes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/oauth-protected-resource/mcp":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"resource": base + "/mcp", "authorization_servers": []string{base}})
		case "/.well-known/oauth-authorization-server":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"issuer": base, "authorization_endpoint": base + "/authorize", "token_endpoint": base + "/token", "response_types_supported": []string{"code"}, "code_challenge_methods_supported": []string{"S256"}, "scopes_supported": []string{"offline_access"}})
		case "/authorize":
			if r.URL.Query().Get("code_challenge_method") != "S256" || r.URL.Query().Get("client_id") != "test-client" {
				t.Errorf("authorization request missing PKCE or client ID: %s", r.URL)
			}
			challenge = r.URL.Query().Get("code_challenge")
			redirect, err := url.Parse(r.URL.Query().Get("redirect_uri"))
			if err != nil {
				t.Error(err)
				return
			}
			q := redirect.Query()
			q.Set("code", "test-code")
			q.Set("state", r.URL.Query().Get("state"))
			redirect.RawQuery = q.Encode()
			http.Redirect(w, r, redirect.String(), http.StatusFound)
		case "/token":
			if err := r.ParseForm(); err != nil {
				t.Error(err)
			}
			w.Header().Set("Content-Type", "application/json")
			switch r.Form.Get("grant_type") {
			case "authorization_code":
				verifier := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
				if base64.RawURLEncoding.EncodeToString(verifier[:]) != challenge {
					t.Error("OAuth code verifier did not match PKCE challenge")
				}
				_, _ = w.Write([]byte(`{"access_token":"access-1","token_type":"Bearer","expires_in":3600,"refresh_token":"refresh-1"}`))
			case "refresh_token":
				refreshes.Add(1)
				if r.Form.Get("refresh_token") != "refresh-1" {
					t.Error("wrong refresh token")
				}
				_, _ = w.Write([]byte(`{"access_token":"access-2","token_type":"Bearer","expires_in":3600,"refresh_token":"refresh-2"}`))
			default:
				http.Error(w, "unknown grant", 400)
			}
		case "/mcp":
			auth := r.Header.Get("Authorization")
			if auth != "Bearer access-1" && auth != "Bearer access-2" {
				w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+base+`/.well-known/oauth-protected-resource/mcp"`)
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return fixtureServer() }, &mcp.StreamableHTTPOptions{JSONResponse: true, Stateless: true}).ServeHTTP(w, r)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	base = server.URL
	config := Config{ID: "oauth-test", Name: "oauth-test", Transport: "http", Endpoint: base + "/mcp", OAuth: true, ClientID: "test-client"}
	if err := Login(context.Background(), config, configDir, func(authURL string) {
		response, err := http.Get(authURL)
		if err != nil {
			t.Error(err)
			return
		}
		response.Body.Close()
	}); err != nil {
		t.Fatal(err)
	}
	path := oauthPath(configDir, config.ID)
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 || filepath.Dir(path) != filepath.Join(configDir, "mcp-oauth") {
		t.Fatalf("credential file: %+v %v", info, err)
	}
	config.Name = "Renamed OAuth service"
	connection, err := Open(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	connection.Close()
	credential, err := readOAuth(path, config.Endpoint)
	if err != nil {
		t.Fatal(err)
	}
	credential.Token.Expiry = time.Now().Add(-time.Hour)
	if err := writeOAuth(path, credential); err != nil {
		t.Fatal(err)
	}
	connection, err = Open(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	result, err := connection.CallTool(context.Background(), "echo", json.RawMessage(`{"text":"after refresh"}`))
	if err != nil || result.IsError || result.Content != "after refresh" {
		t.Fatalf("refreshed OAuth tool call: %+v %v", result, err)
	}
	connection.Close()
	if refreshes.Load() != 1 {
		t.Fatalf("expected one refresh, got %d", refreshes.Load())
	}
	credential, err = readOAuth(path, config.Endpoint)
	if err != nil || credential.Token.AccessToken != "access-2" || credential.Token.RefreshToken != "refresh-2" {
		t.Fatalf("refreshed token was not persisted: %+v %v", credential.Token, err)
	}
}

func TestOAuthDynamicClientRegistration(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	configDir, _ := appdirs.Config()
	var base string
	registered := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/.well-known/oauth-protected-resource/mcp":
			_ = json.NewEncoder(w).Encode(map[string]any{"resource": base + "/mcp", "authorization_servers": []string{base}})
		case "/.well-known/oauth-authorization-server":
			_ = json.NewEncoder(w).Encode(map[string]any{"issuer": base, "authorization_endpoint": base + "/authorize", "token_endpoint": base + "/token", "registration_endpoint": base + "/register", "response_types_supported": []string{"code"}, "code_challenge_methods_supported": []string{"S256"}})
		case "/register":
			registered = true
			var request struct {
				RedirectURIs []string `json:"redirect_uris"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil || len(request.RedirectURIs) != 1 {
				t.Errorf("invalid client registration: %+v %v", request, err)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"client_id": "dynamic-client", "redirect_uris": request.RedirectURIs, "token_endpoint_auth_method": "none"})
		case "/authorize":
			if r.URL.Query().Get("client_id") != "dynamic-client" {
				t.Error("dynamic client ID missing")
			}
			redirect, _ := url.Parse(r.URL.Query().Get("redirect_uri"))
			q := redirect.Query()
			q.Set("code", "dynamic-code")
			q.Set("state", r.URL.Query().Get("state"))
			redirect.RawQuery = q.Encode()
			http.Redirect(w, r, redirect.String(), http.StatusFound)
		case "/token":
			_, _ = w.Write([]byte(`{"access_token":"dynamic-access","token_type":"Bearer","expires_in":3600,"refresh_token":"dynamic-refresh"}`))
		case "/mcp":
			if r.Header.Get("Authorization") != "Bearer dynamic-access" {
				w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+base+`/.well-known/oauth-protected-resource/mcp"`)
				http.Error(w, "unauthorized", 401)
				return
			}
			mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return fixtureServer() }, &mcp.StreamableHTTPOptions{JSONResponse: true, Stateless: true}).ServeHTTP(w, r)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	base = server.URL
	config := Config{ID: "dynamic", Name: "dynamic", Transport: "http", Endpoint: base + "/mcp", OAuth: true}
	if err := Login(context.Background(), config, configDir, func(authURL string) {
		response, err := http.Get(authURL)
		if err != nil {
			t.Error(err)
			return
		}
		response.Body.Close()
	}); err != nil {
		t.Fatal(err)
	}
	if !registered {
		t.Fatal("dynamic client registration was skipped")
	}
	credential, err := readOAuth(oauthPath(configDir, "dynamic"), config.Endpoint)
	if err != nil || credential.Config.ClientID != "dynamic-client" {
		t.Fatalf("dynamic credentials: %+v %v", credential.Config, err)
	}
	connection, err := Open(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	result, err := connection.CallTool(context.Background(), "echo", json.RawMessage(`{"text":"registered"}`))
	if err != nil || result.IsError || result.Content != "registered" {
		t.Fatalf("registered OAuth tool call: %+v %v", result, err)
	}
}
