package provider

import (
	"fmt"
	"maps"
	"os"
	"reflect"
	"testing"
)

// A new service can prepare and validate a native route by registering its own
// adapter. Catalog storage and the model execution protocol table stay unchanged.
func TestCatalogServiceAdapterOwnsRouteEndpointAndCredentials(t *testing.T) {
	const service = "fixture-catalog-adapter"
	const id = "fixture-model"
	const keyName = "FIXTURE_CATALOG_API_KEY"
	if _, err := LookupCatalogProvider("openai"); err != nil {
		t.Fatal(err)
	}
	catalogProviders[service] = CatalogProvider{ID: service, Package: "@ai-sdk/openai-compatible", BaseURL: "https://default.invalid/v1"}
	if !ensureOMPCatalog() {
		t.Fatal(ompCatalogErr)
	}
	original := CurrentOMPCatalog()
	fixture := *original
	fixture.data.Providers = maps.Clone(original.data.Providers)
	fixture.data.Providers[service] = map[string]OMPModel{id: {Name: "Fixture", API: "openai-completions", ContextWindow: 100000, MaxTokens: 4096, Input: []string{"text"}}}
	fixture.models = fixture.data.Providers
	fixture.Activate()
	catalogAdapters[service] = catalogAdapter{
		resolveRoute: func(state *catalogResolveState) error {
			if state.config.Route != "messages" {
				return fmt.Errorf("fixture route %q is unsupported", state.config.Route)
			}
			state.config.Provider = "anthropic"
			return nil
		},
		resolveEndpoint: func(state *catalogResolveState) error {
			if state.config.BaseURL == "" {
				state.config.BaseURL = "https://messages.invalid/v1"
			}
			return nil
		},
		resolveCredential: func(state *catalogResolveState) error {
			if state.config.APIKey == "" {
				state.config.APIKey = os.Getenv(keyName)
				state.credentialEnv = append(state.credentialEnv, keyName)
			}
			return nil
		},
	}
	t.Cleanup(func() {
		delete(catalogProviders, service)
		original.Activate()
		delete(catalogAdapters, service)
	})
	t.Setenv(keyName, "fixture-only-secret")
	for _, endpoint := range []string{"", "http://127.0.0.1:1234/v1"} {
		raw := CatalogConfig{Key: "fixture", Provider: "catalog", ProviderID: service, ID: id, Route: "messages", BaseURL: endpoint}
		resolved, err := ResolveCatalogConfig(raw)
		if err != nil {
			t.Fatal(err)
		}
		if raw.Provider != "catalog" || raw.APIKey != "" || raw.Tools != nil || raw.Context != 0 {
			t.Fatal("provider resolution changed the raw config")
		}
		if resolved.Config.Provider != "anthropic" || resolved.Config.APIKey != "fixture-only-secret" || resolved.Config.Tools == nil || !*resolved.Config.Tools || resolved.Metadata.InputLimit != 0 || resolved.Metadata.OutputLimit != 4096 || !reflect.DeepEqual(resolved.CredentialEnvironment, []string{keyName}) {
			t.Fatal("service adapter did not preserve route, capability or credential state")
		}
		wantEndpoint := endpoint
		if wantEndpoint == "" {
			wantEndpoint = "https://messages.invalid/v1"
		}
		if resolved.Config.BaseURL != wantEndpoint || resolved.Metadata.BaseURLExplicit != (endpoint != "") {
			t.Fatalf("provider endpoint override changed: %q", resolved.Config.BaseURL)
		}
		if err := ValidateCatalogOptions(resolved.Config, resolved.Metadata); err != nil {
			t.Fatalf("registered route needs a second service whitelist: %v", err)
		}
		if err := ValidateCatalogProtocol(resolved.Config, resolved.Metadata); err != nil {
			t.Fatalf("registered route cannot execute: %v", err)
		}
		request := CatalogModelRequest(resolved.Config, resolved.Metadata)
		if request.Provider != "anthropic" || request.BaseURL != wantEndpoint || request.APIKey != "fixture-only-secret" || request.ID != id {
			t.Fatal("prepared service differs from its model request")
		}
	}
	if _, err := ResolveCatalogConfig(CatalogConfig{Key: "fixture", Provider: "catalog", ProviderID: service, ID: id, Route: "unsupported"}); err == nil {
		t.Fatal("adapter route failure was ignored")
	}
}
