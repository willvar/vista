package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"vista/internal/copilotauth"
	"vista/internal/modelcatalog"
)

func TestCopilotBrowserLoginLifecycle(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/login/device/code":
			fmt.Fprintf(w, `{"verification_uri":%q,"user_code":"ABCD-EFGH","device_code":"secret-device","interval":1,"expires_in":30}`, "http://"+r.Host+"/verify")
		case "/login/oauth/access_token":
			fmt.Fprint(w, `{"access_token":"secret-oauth"}`)
		case "/copilot_internal/user":
			fmt.Fprintf(w, `{"chat_enabled":true,"endpoints":{"api":%q}}`, "http://"+r.Host)
		case "/models":
			fmt.Fprint(w, `{"data":[{"id":"gpt-6-sol","model_picker_enabled":true,"supported_endpoints":["/responses"],"capabilities":{"limits":{"max_prompt_tokens":128000,"max_output_tokens":16000},"supports":{"tool_calls":true}}}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	dir := t.TempDir()
	_ = os.Chmod(dir, 0700)
	base := modelCatalog{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	app := &App{ctx: ctx, configDir: dir, models: base, baseModels: base, copilotAuthClient: &copilotauth.Client{Issuer: upstream.URL, HTTP: upstream.Client()}}
	call := func(method, path, body string) providerLogin {
		t.Helper()
		w := httptest.NewRecorder()
		r := httptest.NewRequest(method, "/api/provider-auth/copilot"+path, strings.NewReader(body))
		if method == http.MethodPost {
			app.startProviderLogin(w, r)
		} else {
			app.providerLoginStatus(w, r)
		}
		if w.Code != 200 || strings.Contains(w.Body.String(), "secret-") {
			t.Fatalf("public login response: %s", w.Body.String())
		}
		var login providerLogin
		if err := json.Unmarshal(w.Body.Bytes(), &login); err != nil {
			t.Fatal(err)
		}
		return login
	}
	wait := func(id string) {
		t.Helper()
		for deadline := time.Now().Add(8 * time.Second); time.Now().Before(deadline); {
			login := call(http.MethodGet, "/"+id, "")
			if login.Status == "connected" {
				return
			}
			if login.Status != "pending" {
				t.Fatalf("login failed: %+v", login)
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("login did not finish")
	}
	pending := call(http.MethodPost, "", `{"name":"Copilot account"}`)
	if call(http.MethodDelete, "/"+pending.ID, "").Status != "canceled" {
		t.Fatal("cancel failed")
	}
	if _, exists, _ := modelcatalog.Read(dir); exists {
		t.Fatal("canceled login created connection")
	}
	login := call(http.MethodPost, "", `{"name":"Copilot account"}`)
	wait(login.ID)
	raw, _, err := modelcatalog.Read(dir)
	if err != nil || len(raw.Models) != 1 {
		t.Fatal("login did not save connection")
	}
	anchor := raw.Models[0]
	loaded, err := modelcatalog.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	model, _, err := loaded.Resolve(anchor.Key)
	if err != nil || model.APIKey != "secret-oauth" || model.Provider != "openai-responses" {
		t.Fatal("reloaded connection is not executable")
	}
	w := httptest.NewRecorder()
	app.modelSettings(w, httptest.NewRequest(http.MethodGet, "/api/model-config", nil))
	if strings.Contains(w.Body.String(), "secret-") || !strings.Contains(w.Body.String(), `"credentialMode":"oauth"`) {
		t.Fatal("incorrect public auth settings")
	}
	login = call(http.MethodPost, "", fmt.Sprintf(`{"name":"Renamed","key":%q}`, anchor.Key))
	wait(login.ID)
	raw, _, err = modelcatalog.Read(dir)
	if err != nil || len(raw.Models) != 1 || raw.Models[0].ConnectionID != anchor.ConnectionID || raw.Models[0].Name != "Renamed" {
		t.Fatal("reconnect changed connection identity")
	}
	snapshot, err := app.modelSettingsSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	raw, err = modelcatalog.ChangeConnection(ctx, snapshot.raw, snapshot.exists, http.MethodDelete, modelcatalog.ConnectionInput{Key: anchor.Key})
	if err != nil {
		t.Fatal(err)
	}
	if err := app.commitModelSettings(ctx, snapshot, raw); err != nil {
		t.Fatal(err)
	}
	saved, _, err := modelcatalog.Read(dir)
	if err != nil || len(saved.Models) != 0 {
		t.Fatal("disconnect retained account")
	}
}

func TestCopilotAdvancedLoginSettingsPersistAndReconnect(t *testing.T) {
	fixture := func(expectedClient string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			if strings.HasPrefix(r.URL.Path, "/login/") {
				_ = r.ParseForm()
				id := r.Form.Get("client_id")
				if id == "" || expectedClient != "" && id != expectedClient || expectedClient == "" && id == "enterprise-app" {
					t.Errorf("wrong OAuth app for this account: %q", id)
				}
			}
			switch r.URL.Path {
			case "/login/device/code":
				fmt.Fprintf(w, `{"verification_uri":%q,"user_code":"CODE","device_code":"fixture-device","interval":1,"expires_in":30}`, "http://"+r.Host+"/verify")
			case "/login/oauth/access_token":
				fmt.Fprint(w, `{"access_token":"private-copilot-token"}`)
			case "/copilot_internal/user":
				fmt.Fprintf(w, `{"chat_enabled":true,"endpoints":{"api":%q}}`, "http://"+r.Host)
			case "/models":
				fmt.Fprint(w, `{"data":[{"id":"gpt-6-sol","model_picker_enabled":true,"supported_endpoints":["/responses"],"capabilities":{"limits":{"max_prompt_tokens":128000,"max_output_tokens":16000},"supports":{"tool_calls":true}}}]}`)
			default:
				http.NotFound(w, r)
			}
		}))
	}
	standard, enterprise := fixture(""), fixture("enterprise-app")
	defer standard.Close()
	defer enterprise.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	base := modelCatalog{}
	client := &copilotauth.Client{Issuer: standard.URL, HTTP: standard.Client()}
	app := &App{ctx: ctx, configDir: dir, models: base, baseModels: base, copilotAuthClient: client}
	call := func(method, id string, input any) providerLogin {
		t.Helper()
		body, _ := json.Marshal(input)
		w := httptest.NewRecorder()
		r := httptest.NewRequest(method, "/api/provider-auth/copilot"+id, strings.NewReader(string(body)))
		if method == http.MethodPost {
			app.startProviderLogin(w, r)
		} else {
			app.providerLoginStatus(w, r)
		}
		var login providerLogin
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &login) != nil || strings.Contains(w.Body.String(), "private-copilot-token") {
			t.Fatalf("login response: %d %s", w.Code, w.Body.String())
		}
		return login
	}
	key, connectionID := "", ""
	for _, options := range []copilotauth.Options{{EnterpriseURL: enterprise.URL, ClientID: "enterprise-app"}, {}} {
		login := call(http.MethodPost, "", map[string]any{"key": key, "copilotAuth": options})
		if client.Issuer != standard.URL || client.ClientID != "" {
			t.Fatal("login changed the shared client")
		}
		if key == "" {
			// A simultaneous ordinary account must use the default OAuth app.
			ordinary := call(http.MethodPost, "", map[string]string{})
			call(http.MethodDelete, "/"+ordinary.ID, nil)
		}
		for deadline := time.Now().Add(8 * time.Second); login.Status == "pending" && time.Now().Before(deadline); {
			time.Sleep(10 * time.Millisecond)
			login = call(http.MethodGet, "/"+login.ID, nil)
		}
		if login.Status != "connected" {
			t.Fatalf("login did not complete: %+v", login)
		}
		raw, _, err := modelcatalog.Read(dir)
		if err != nil || len(raw.Models) != 1 {
			t.Fatalf("saved connection: %+v %v", raw, err)
		}
		anchor := raw.Models[0]
		if !modelcatalog.Editable(anchor) || anchor.CopilotAuth == nil || *anchor.CopilotAuth != options || connectionID != "" && anchor.ConnectionID != connectionID {
			t.Fatalf("login options or connection identity lost: %+v", anchor.CopilotAuth)
		}
		key, connectionID = anchor.Key, anchor.ConnectionID
		edit := modelcatalog.ConnectionInput{Key: key, Name: "Renamed", Provider: "catalog", ProviderID: "github-copilot", BaseURL: anchor.BaseURL, CredentialMode: "oauth"}
		changed, err := modelcatalog.ChangeConnection(ctx, raw, true, http.MethodPut, edit)
		if err != nil || changed.Models[0].CopilotAuth == nil || *changed.Models[0].CopilotAuth != options {
			t.Fatal("ordinary edits lost login options", err)
		}
		edit.CopilotAuth = &copilotauth.Options{ClientID: "another-app"}
		if _, err := modelcatalog.ChangeConnection(ctx, raw, true, http.MethodPut, edit); err == nil {
			t.Fatal("login settings changed while reusing an existing credential")
		}
		loaded, err := modelcatalog.Load(dir)
		if err != nil {
			t.Fatal(err)
		}
		// Reload from disk, then reconnect without resubmitting advanced fields.
		app = &App{ctx: ctx, configDir: dir, models: loaded, baseModels: loaded, copilotAuthClient: client}
		reconnect := call(http.MethodPost, "", map[string]string{"key": key})
		call(http.MethodDelete, "/"+reconnect.ID, nil)
		w := httptest.NewRecorder()
		app.modelSettings(w, httptest.NewRequest(http.MethodGet, "/api/model-config", nil))
		var settings struct {
			Models []modelSetting `json:"models"`
		}
		if json.Unmarshal(w.Body.Bytes(), &settings) != nil || len(settings.Models) != 1 || settings.Models[0].CopilotAuth == nil || *settings.Models[0].CopilotAuth != options || strings.Contains(w.Body.String(), "private-copilot-token") {
			t.Fatal("settings did not return safe, editable login options")
		}
	}
}
