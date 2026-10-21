package modelcatalog

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"vista/internal/provider"
)

func TestBuiltInSetupsRequireOMPModels(t *testing.T) {
	for _, setup := range ProviderSetups() {
		if setup.ID == "custom" || LocalProvider(setup.ID) {
			continue
		}
		if len(provider.CatalogTextModelIDs(setup.ID)) == 0 {
			t.Fatalf("built-in provider has no OMP text models: %s", setup.ID)
		}
	}
	for _, id := range []string{"cohere", "alibaba", "alibaba-cn", "zhipuai", "siliconflow", "alibaba-coding-plan-cn"} {
		if _, ok := setupFor(id); ok {
			t.Fatalf("provider without OMP metadata has a built-in entry: %s", id)
		}
		if _, err := ChangeConnection(context.Background(), Catalog{}, false, http.MethodPost, ConnectionInput{Provider: "catalog", ProviderID: id, APIKey: "test-key"}); err == nil {
			t.Fatalf("removed built-in entry is still accepted: %s", id)
		}
	}
}

func TestCatalogProviderSetupLifecycle(t *testing.T) {
	for _, setup := range ProviderSetups() {
		if automaticSetup(setup.ID) {
			continue
		}
		t.Run(setup.ID, func(t *testing.T) {
			input := ConnectionInput{Provider: "catalog", ProviderID: setup.ID, APIKey: "test-key", CredentialMode: "apiKey"}
			switch setup.ID {
			case "openai-codex":
				input.APIKey, input.CredentialMode = "", "oauth"
			case "azure":
				if len(setup.Models) == 0 {
					t.Fatal("Azure deployment has no model mappings")
				}
				input.IDs = []string{setup.Models[0].ID}
				input.ResourceName, input.DeploymentID = "test-resource", "production-deployment"
			case "amazon-bedrock":
				input.Region = "us-east-1"
			case "google-vertex":
				input.Project, input.Location = "test-project", "us-central1"
				input.APIKey, input.CredentialMode = "", "default"
			}
			ctx := context.Background()
			raw, err := ChangeConnection(ctx, Catalog{}, false, http.MethodPost, input)
			if err != nil {
				t.Fatal(err)
			}
			if len(raw.Models) != 1 {
				t.Fatal("connection serialized the model inventory")
			}
			dir := t.TempDir()
			if err := os.Chmod(dir, 0700); err != nil {
				t.Fatal(err)
			}
			resolved, err := Save(dir, raw)
			if err != nil {
				t.Fatalf("connection is not executable: %v", err)
			}
			if catalogInventorySetup(setup.ID) && (len(setup.Models) != 0 || len(resolved.Models) < 2) {
				t.Fatal("provider still requires setup selection or did not expose its inventory")
			}
			model, _, err := resolved.Resolve(raw.Default)
			if err != nil || model.Provider == "catalog" {
				t.Fatalf("route was not resolved: %s, %v", model.Provider, err)
			}
			if setup.ID == "azure" && (model.WireID != input.DeploymentID || model.ID != input.IDs[0] || !model.AzureNative) {
				t.Fatalf("Azure lost its model/deployment mapping: %+v", model)
			}
			anchor := raw.Models[0]
			if !Editable(anchor) || SetupID(anchor) != setup.ID {
				t.Fatal("saved connection cannot be edited")
			}
			input.Key, input.Name, input.APIKey = anchor.Key, "Renamed", ""
			edited, err := ChangeConnection(ctx, raw, true, http.MethodPut, input)
			if err != nil || edited.Models[0].APIKey != anchor.APIKey || edited.Models[0].ConnectionID != anchor.ConnectionID || edited.Models[0].Key != anchor.Key {
				t.Fatalf("edit lost credential or identity: %v", err)
			}
			if _, err := Save(dir, edited); err != nil {
				t.Fatal(err)
			}
			loaded, err := Load(dir)
			if err != nil || loaded.Default != anchor.Key || loaded.Models[0].Name != "Renamed" {
				t.Fatalf("reload lost the edit: %v", err)
			}
			if len(loaded.Models) != len(resolved.Models) {
				t.Fatal("inventory changed after rename and reload")
			}
			for index, model := range loaded.Models {
				if model.Key != resolved.Models[index].Key || model.Name != "Renamed" {
					t.Fatal("inventory lost a model identity or the connection rename")
				}
			}
			removed, err := ChangeConnection(ctx, edited, true, http.MethodDelete, ConnectionInput{Key: anchor.Key})
			if err != nil || len(removed.Models) != 0 {
				t.Fatalf("disconnect failed: %v", err)
			}
		})
	}
}

func TestSetupCredentialsStayWithinDestinationAndMode(t *testing.T) {
	ctx := context.Background()
	input := ConnectionInput{Provider: "catalog", ProviderID: "google-vertex", CredentialMode: "accessToken", AccessToken: "saved-token", Project: "first-project", Location: "us-central1", IDs: []string{"gemini-2.5-flash"}}
	raw, err := ChangeConnection(ctx, Catalog{}, false, http.MethodPost, input)
	if err != nil {
		t.Fatal(err)
	}
	input.Key, input.AccessToken, input.Project = raw.Models[0].Key, "", "second-project"
	if _, err := ChangeConnection(ctx, raw, true, http.MethodPut, input); err == nil {
		t.Fatal("reused token across project boundary")
	}
	input.Project = "first-project"
	edited, err := ChangeConnection(ctx, raw, true, http.MethodPut, input)
	if err != nil || edited.Models[0].AccessToken != "saved-token" {
		t.Fatalf("same-project edit lost its token: %v", err)
	}
	input.CredentialMode = "default"
	edited, err = ChangeConnection(ctx, edited, true, http.MethodPut, input)
	if err != nil || edited.Models[0].AccessToken != "" || edited.Models[0].APIKey != "" {
		t.Fatalf("switch to ADC retained explicit credentials: %v", err)
	}
	input.ProviderID = "deepseek"
	if _, err := ChangeConnection(ctx, raw, true, http.MethodPut, input); err == nil {
		t.Fatal("changed an existing connection's provider type")
	}
}

func TestLocalSetupDiscoversWithoutAKey(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("sent a credential for an unauthenticated endpoint")
		}
		switch r.URL.Path {
		case "/health":
			w.WriteHeader(http.StatusOK)
		case "/v1/models":
			_, _ = w.Write([]byte(`{"data":[{"id":"local-model","owned_by":"vllm","max_model_len":32768}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	input := ConnectionInput{Provider: "catalog", ProviderID: "vllm", BaseURL: upstream.URL + "/v1", CredentialMode: "default"}
	raw, err := ChangeConnection(context.Background(), Catalog{}, false, http.MethodPost, input)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	_ = os.Chmod(dir, 0700)
	if _, err := Save(dir, raw); err != nil {
		t.Fatal(err)
	}
	model := raw.Models[0]
	if model.ID != "local-model" || model.APIKey != "" || model.Tools != nil || !Editable(model) {
		t.Fatalf("incorrect local connection: %+v", model)
	}
	input.Key, input.Name = model.Key, "Local inference"
	if _, err := ChangeConnection(context.Background(), raw, true, http.MethodPut, input); err != nil {
		t.Fatal(err)
	}
}

func TestCopilotSetupUsesAccountInventory(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" || r.Header.Get("Authorization") != "Bearer copilot-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"new-copilot-model","model_picker_enabled":true,"supported_endpoints":["/v1/messages"],"capabilities":{"limits":{"max_prompt_tokens":100000,"max_output_tokens":4096},"supports":{"tool_calls":true}}}]}`))
	}))
	defer upstream.Close()
	input := ConnectionInput{Provider: "catalog", ProviderID: "github-copilot", BaseURL: upstream.URL, APIKey: "copilot-token"}
	raw, err := ChangeConnection(context.Background(), Catalog{}, false, http.MethodPost, input)
	if err != nil {
		t.Fatal(err)
	}
	model := raw.Models[0]
	if model.Provider != "anthropic" || model.ID != "new-copilot-model" || !Editable(model) || model.BaseURL != upstream.URL+"/v1" {
		t.Fatal("Copilot did not use the advertised route")
	}
	input.Key, input.APIKey = model.Key, ""
	if _, err := ChangeConnection(context.Background(), raw, true, http.MethodPut, input); err != nil {
		t.Fatalf("edit failed to reuse credential at the same root: %v", err)
	}
	other := model
	other.ConnectionID = "other-account"
	if CopilotSourceID(model) == CopilotSourceID(other) {
		t.Fatal("Copilot sources merged separate connections")
	}
	models, err := DiscoverCopilotModels(context.Background(), model)
	if err != nil || models[0].ConnectionID != model.ConnectionID || models[0].Name != model.Name {
		t.Fatalf("discovery lost connection identity: %v", err)
	}
	model.Name = "Renamed Copilot"
	renamed, err := DiscoverCopilotModels(context.Background(), model)
	if err != nil || renamed[0].Key != models[0].Key {
		t.Fatalf("rename changed a session's selected model key: %v", err)
	}
}

func TestSetupLeavesAdvancedFileConfigurationUneditable(t *testing.T) {
	for _, model := range []Configured{
		{Provider: "catalog", ProviderID: "anthropic", BaseURL: "https://private-gateway.example/v1"},
		{Provider: "catalog", ProviderID: "alibaba", Region: "cn-hongkong"},
	} {
		if Editable(model) {
			t.Fatalf("form would drop advanced configuration for %s", model.ProviderID)
		}
	}
}

func TestVertexSetupSelectsModelsCompatibleWithAuthentication(t *testing.T) {
	for _, mode := range []string{"apiKey", "default", "accessToken"} {
		t.Run(mode, func(t *testing.T) {
			input := ConnectionInput{Provider: "catalog", ProviderID: "google-vertex", CredentialMode: mode}
			switch mode {
			case "apiKey":
				input.APIKey = "express-key"
			case "default":
				input.Project = "project"
			case "accessToken":
				input.Project = "project"
				input.AccessToken = "token"
			}
			raw, err := ChangeConnection(context.Background(), Catalog{}, false, http.MethodPost, input)
			if err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			_ = os.Chmod(dir, 0700)
			catalog, err := Save(dir, raw)
			if err != nil {
				t.Fatal(err)
			}
			protocols := map[string]bool{}
			for _, entry := range catalog.Models {
				model, _, err := catalog.Resolve(entry.Key)
				if err != nil {
					t.Fatal(err)
				}
				protocols[model.Provider] = true
				if mode == "apiKey" && (model.Provider != "vertex-gemini" || !model.VertexAPIKey) {
					t.Fatalf("incompatible model exposed in express mode: %s", entry.ID)
				}
			}
			if len(catalog.Models) < 2 {
				t.Fatal("missing inventory")
			}
			if mode != "apiKey" && (!protocols["vertex-gemini"] || !protocols["vertex-anthropic"] || !protocols["openai-chat"]) {
				t.Fatalf("Vertex connection is missing a model family: %v", protocols)
			}
		})
	}
}

func TestVertexAuthenticationChangeReplacesIncompatibleAnchor(t *testing.T) {
	input := ConnectionInput{Provider: "catalog", ProviderID: "google-vertex", CredentialMode: "default", Project: "project"}
	raw, err := ChangeConnection(context.Background(), Catalog{}, false, http.MethodPost, input)
	if err != nil {
		t.Fatal(err)
	}
	original := raw.Models[0]
	if !strings.HasPrefix(original.ID, "claude-") {
		t.Fatal("fixture should exercise a Claude anchor")
	}
	input.Key, input.CredentialMode, input.APIKey = original.Key, "apiKey", "express-key"
	updated, err := ChangeConnection(context.Background(), raw, true, http.MethodPut, input)
	if err != nil {
		t.Fatal(err)
	}
	if len(updated.Models) != 1 || !strings.HasPrefix(updated.Models[0].ID, "gemini-") || updated.Models[0].ConnectionID != original.ConnectionID || updated.Default != updated.Models[0].Key {
		t.Fatal("auth mode change retained incompatible anchor or lost default connection")
	}
	dir := t.TempDir()
	_ = os.Chmod(dir, 0700)
	if _, err := Save(dir, updated); err != nil {
		t.Fatal(err)
	}
}
