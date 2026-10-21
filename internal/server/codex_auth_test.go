package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"vista/internal/codexauth"
	"vista/internal/modelcatalog"
	"vista/internal/provider"
)

func codexTestApp(t *testing.T, ready <-chan struct{}) *App {
	t.Helper()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/accounts/deviceauth/usercode":
			fmt.Fprint(w, `{"device_auth_id":"private-device","user_code":"ABCD-EFGH","interval":"1"}`)
		case "/api/accounts/deviceauth/token":
			select {
			case <-ready:
			case <-r.Context().Done():
				return
			}
			fmt.Fprint(w, `{"authorization_code":"code","code_verifier":"verifier"}`)
		case "/oauth/token":
			claims, _ := json.Marshal(map[string]any{"exp": time.Now().Add(time.Hour).Unix(), "https://api.openai.com/auth": map[string]string{"chatgpt_account_id": "account"}})
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "header." + base64.RawURLEncoding.EncodeToString(claims) + ".signature", "refresh_token": "secret-refresh", "expires_in": 3600})
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(upstream.Close)
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	base := modelCatalog{Default: "demo", Models: []configuredModel{{Key: "demo"}}}
	app := &App{configDir: dir, models: base, baseModels: base, codexAuthClient: &codexauth.Client{Issuer: upstream.URL, HTTP: upstream.Client()}}
	t.Cleanup(func() {
		app.providerLoginMu.Lock()
		defer app.providerLoginMu.Unlock()
		for _, login := range app.providerLogins {
			login.cancel()
		}
	})
	return app
}

func startTestCodexLogin(t *testing.T, app *App, input string) providerLogin {
	t.Helper()
	w := httptest.NewRecorder()
	app.startProviderLogin(w, httptest.NewRequest(http.MethodPost, "/api/provider-auth/codex", strings.NewReader(input)))
	if w.Code != 200 {
		t.Fatalf("start failed: %s", w.Body.String())
	}
	if strings.Contains(w.Body.String(), "private-device") || strings.Contains(w.Body.String(), "secret-refresh") {
		t.Fatal("secret in login response")
	}
	var login providerLogin
	if err := json.Unmarshal(w.Body.Bytes(), &login); err != nil {
		t.Fatal(err)
	}
	if login.ID == "" || login.UserCode != "ABCD-EFGH" {
		t.Fatalf("missing challenge: %s", w.Body.String())
	}
	return login
}

func awaitTestCodexLogin(t *testing.T, app *App, id string) providerLogin {
	t.Helper()
	for deadline := time.Now().Add(4 * time.Second); time.Now().Before(deadline); {
		w := httptest.NewRecorder()
		app.providerLoginStatus(w, httptest.NewRequest(http.MethodGet, "/api/provider-auth/codex/"+id, nil))
		var login providerLogin
		if err := json.Unmarshal(w.Body.Bytes(), &login); err != nil {
			t.Fatal(err)
		}
		if login.Status != "pending" {
			return login
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("login never finished")
	return providerLogin{}
}

func TestCodexLoginConnectReloadReauthenticateAndDisconnect(t *testing.T) {
	ready := make(chan struct{})
	close(ready)
	app := codexTestApp(t, ready)
	login := startTestCodexLogin(t, app, `{"name":"My Codex"}`)
	if result := awaitTestCodexLogin(t, app, login.ID); result.Status != "connected" {
		t.Fatalf("login failed: %+v", result)
	}
	ctx := context.Background()
	raw, _, err := modelcatalog.Read(app.configDir)
	if err != nil || len(raw.Models) != 1 {
		t.Fatalf("connection not saved: %v", err)
	}
	anchor := raw.Models[0]
	path := codexauth.Path(app.configDir, anchor.ConnectionID)
	if anchor.APIKey != "" {
		t.Fatal("tokens stored in models.json")
	}
	w := httptest.NewRecorder()
	app.modelSettings(w, httptest.NewRequest(http.MethodGet, "/api/model-config", nil))
	if strings.Contains(w.Body.String(), "signature") || strings.Contains(w.Body.String(), "secret-refresh") || !strings.Contains(w.Body.String(), `"credentialMode":"oauth"`) {
		t.Fatalf("incorrect public settings: %s", w.Body.String())
	}
	loaded, err := modelcatalog.Load(app.configDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Models) != len(provider.CatalogTextModelIDs("openai-codex")) || len(loaded.Models) < 2 {
		t.Fatal("Codex models not restored")
	}
	for _, configured := range loaded.Models {
		model, _, err := loaded.Resolve(configured.Key)
		if err != nil || model.CodexCredentialPath != path || model.ResponsesRoute != "codex" || model.Provider != "openai-responses" || model.APIKey != "" || len(configured.Info().ReasoningOptions) == 0 {
			t.Fatalf("incorrect executable Codex model: %s %v", configured.ID, err)
		}
		if configured.ID == "gpt-image-2" {
			t.Fatal("non-text model in conversation picker")
		}
		if err := modelcatalog.ApplyReasoningEffort(&model, "max"); err != nil || model.ReasoningEffort != "max" {
			t.Fatalf("Codex reasoning selection is unusable: %v", err)
		}
	}
	selected := loaded.Models[1]
	if err := app.selectDefaultModel(ctx, selected.Key); err != nil {
		t.Fatal(err)
	}
	login = startTestCodexLogin(t, app, fmt.Sprintf(`{"key":%q,"name":"Renamed Codex"}`, anchor.Key))
	if result := awaitTestCodexLogin(t, app, login.ID); result.Status != "connected" {
		t.Fatalf("reconnect failed: %+v", result)
	}
	reloaded, err := modelcatalog.Load(app.configDir)
	if err != nil || reloaded.Default != selected.Key || len(reloaded.Models) != len(loaded.Models) || reloaded.Models[0].ConnectionID != anchor.ConnectionID {
		t.Fatalf("reconnect changed default or connection: %v", err)
	}
	if err := app.changeProviderSettings(ctx, http.MethodPut, providerSettingsInput{Key: anchor.Key, Name: "Final name", Provider: "catalog", ProviderID: "openai-codex", CredentialMode: "oauth"}); err != nil {
		t.Fatal(err)
	}
	if err := app.changeProviderSettings(ctx, http.MethodDelete, providerSettingsInput{Key: anchor.Key}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("disconnect retained account credential")
	}
	if models := app.modelsSnapshot(); len(models.Models) != 1 || models.Models[0].Key != "demo" {
		t.Fatal("disconnect retained Codex inventory")
	}
}

func TestCodexCanceledLoginCannotConnectLater(t *testing.T) {
	ready := make(chan struct{})
	app := codexTestApp(t, ready)
	login := startTestCodexLogin(t, app, `{}`)
	w := httptest.NewRecorder()
	app.providerLoginStatus(w, httptest.NewRequest(http.MethodDelete, "/api/provider-auth/codex/"+login.ID, nil))
	close(ready)
	if result := awaitTestCodexLogin(t, app, login.ID); result.Status != "canceled" {
		t.Fatalf("cancel failed: %+v", result)
	}
	if _, exists, _ := modelcatalog.Read(app.configDir); exists {
		t.Fatal("canceled login created a connection")
	}
	w = httptest.NewRecorder()
	app.providerSettings(w, httptest.NewRequest(http.MethodPost, "/api/provider-config", strings.NewReader(`{"provider":"catalog","providerId":"openai-codex","credentialMode":"oauth"}`)))
	if w.Code != 400 {
		t.Fatal("created account connection without login")
	}
}
