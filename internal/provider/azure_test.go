package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	modelcfg "vista/internal/model"
)

func TestAzureDeploymentNameIsUsedInRequests(t *testing.T) {
	for _, deploymentURLs := range []bool{false, true} {
		t.Run(map[bool]string{false: "v1", true: "deployment"}[deploymentURLs], func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				want := "/responses"
				if deploymentURLs {
					want = "/deployments/production-gpt/responses"
				}
				var body struct {
					Model string `json:"model"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if r.URL.Path != want || body.Model != "production-gpt" || r.Header.Get("api-key") != "test-key" {
					t.Errorf("wrong deployment request: %s, model=%q", r.URL.Path, body.Model)
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = w.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"item_id\":\"reply\",\"delta\":\"Hello\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[]}}\n\n"))
			}))
			defer server.Close()
			resolved, err := ResolveCatalogConfig(CatalogConfig{Provider: "catalog", ProviderID: "azure", ID: "gpt-4.1", DeploymentID: "production-gpt", BaseURL: server.URL, APIKey: "test-key", UseDeploymentURLs: &deploymentURLs})
			if err != nil {
				t.Fatal(err)
			}
			model := CatalogModelRequest(resolved.Config, resolved.Metadata)
			turn, err := OpenAIResponsesTurn(context.Background(), model, []ChatMessage{{Role: "user", Content: "Hi"}}, nil)
			if err != nil || turn.Text != "Hello" || model.ID != "gpt-4.1" {
				t.Fatalf("Azure turn: %s, %v", turn.Text, err)
			}
		})
	}
}

func TestAzureDefaultAPIVersionOnlyOnAzureOpenAIHost(t *testing.T) {
	model := modelcfg.Model{ProviderID: "azure", AzureNative: true}
	for _, check := range []struct{ base, want string }{
		{"https://resource.openai.azure.com/openai", "https://resource.openai.azure.com/openai/v1/responses?api-version=v1"},
		{"https://resource.openai.azure.com/openai/v1", "https://resource.openai.azure.com/openai/v1/responses?api-version=v1"},
		{"https://example.com/openai/v1", "https://example.com/openai/v1/responses"},
	} {
		base, err := url.Parse(check.base)
		if err != nil {
			t.Fatal(err)
		}
		if got := openAIEndpoint(base, "/responses", model); got != check.want {
			t.Fatalf("Azure endpoint %q: %q, want %q", check.base, got, check.want)
		}
	}
}

func TestAzureExplicitAPIVersionOnCustomHost(t *testing.T) {
	base, err := url.Parse("http://127.0.0.1:1234/openai/v1")
	if err != nil {
		t.Fatal(err)
	}
	model := modelcfg.Model{ProviderID: "azure", AzureNative: true, APIVersion: "2026-01-01"}
	if got, want := openAIEndpoint(base, "/responses", model), "http://127.0.0.1:1234/openai/v1/responses?api-version=2026-01-01"; got != want {
		t.Fatalf("Azure custom endpoint: %q, want %q", got, want)
	}
}
