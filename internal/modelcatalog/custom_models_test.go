package modelcatalog

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestCustomDiscoveryUsesExplicitFormatAndAuthentication(t *testing.T) {
	for _, protocol := range []string{"openai-chat", "openai-responses", "anthropic", "gemini", "cohere"} {
		for _, authentication := range []string{"none", "bearer", "x-api-key", "x-goog-api-key"} {
			t.Run(protocol+"/"+authentication, func(t *testing.T) {
				pages := 0
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					pages++
					if r.Method != http.MethodGet || r.Header.Get("X-Workspace") != "saved-workspace" {
						t.Error("discovery lost headers or generated output")
					}
					for _, name := range []string{"Authorization", "x-api-key", "x-goog-api-key"} {
						want := ""
						if name == authentication {
							want = "key"
						}
						if name == "Authorization" && authentication == "bearer" {
							want = "Bearer key"
						}
						if r.Header.Get(name) != want {
							t.Errorf("wrong authentication: %s", name)
						}
					}
					if r.URL.Path != "/prefix/models" {
						t.Errorf("discovery changed explicit prefix: %s", r.URL.Path)
					}
					switch protocol {
					case "anthropic":
						if r.Header.Get("Anthropic-Version") == "" {
							t.Error("no API version")
						}
						if r.URL.Query().Get("after_id") == "" {
							_, _ = w.Write([]byte(`{"data":[{"id":"alias-a"}],"has_more":true,"last_id":"alias-a"}`))
						} else {
							_, _ = w.Write([]byte(`{"data":[{"id":"alias-b"}]}`))
						}
					case "gemini":
						if r.URL.Query().Get("pageToken") == "" {
							_, _ = w.Write([]byte(`{"models":[{"name":"models/alias-a","supportedGenerationMethods":["generateContent"]},{"name":"models/embed","supportedGenerationMethods":["embedContent"]}],"nextPageToken":"next"}`))
						} else {
							_, _ = w.Write([]byte(`{"models":[{"name":"models/alias-b"}]}`))
						}
					case "cohere":
						if r.URL.Query().Get("endpoint") != "chat" {
							t.Error("Cohere inventory lost endpoint filter")
						}
						if r.URL.Query().Get("page_token") == "" {
							_, _ = w.Write([]byte(`{"models":[{"name":"alias-a","endpoints":["chat"]},{"name":"embed","endpoints":["embed"]}],"next_page_token":"next"}`))
						} else {
							_, _ = w.Write([]byte(`{"models":[{"name":"alias-b","endpoints":["chat"]}]}`))
						}
					default:
						_, _ = w.Write([]byte(`{"data":[{"id":"alias-a","supported_endpoints":["/v1/messages"]},{"id":"alias-b","api":"gemini"}]}`))
					}
				}))
				defer upstream.Close()
				key := "key"
				if authentication == "none" {
					key = ""
				}
				models, err := DiscoverCustomModels(context.Background(), Configured{Provider: protocol, ProviderID: "custom", Authentication: authentication, BaseURL: upstream.URL + "/prefix", APIKey: key, Headers: map[string]string{"X-Workspace": "saved-workspace"}})
				if err != nil || len(models) != 2 {
					t.Fatalf("models=%d err=%v", len(models), err)
				}
				if protocol != "openai-chat" && protocol != "openai-responses" && pages != 2 {
					t.Fatalf("pages=%d", pages)
				}
				for _, model := range models {
					if model.Provider != protocol || model.Authentication != authentication {
						t.Error("inventory metadata changed explicit settings")
					}
				}
			})
		}
	}
}

func TestCustomAnonymousConnectionHeadersSaveEditAndReload(t *testing.T) {
	var expectedAuth string
	expectedHeader := "secret-header"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != expectedAuth || r.Header.Get("X-Access") != expectedHeader {
			t.Error("wrong discovery credentials")
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"alias","supported_endpoints":["/v1/chat/completions"]}]}`))
	}))
	defer upstream.Close()
	input := ConnectionInput{Provider: "openai-chat", ProviderID: "custom", BaseURL: upstream.URL, CredentialMode: "none", Headers: map[string]string{"x-access": "secret-header"}}
	raw, err := ChangeConnection(context.Background(), Catalog{}, false, http.MethodPost, input)
	if err != nil {
		t.Fatal(err)
	}
	anchor := raw.Models[0]
	if anchor.Name != "127.0.0.1" || anchor.Provider != "openai-chat" || anchor.Authentication != "none" || anchor.APIKey != "" || !Editable(anchor) {
		t.Fatalf("bad connection: %+v", anchor)
	}
	dir := t.TempDir()
	_ = os.Chmod(dir, 0700)
	if _, err = Save(dir, raw); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(dir)
	if err != nil || loaded.Models[0].Headers["X-Access"] != "secret-header" {
		t.Fatalf("lost saved headers: %v", err)
	}
	input.Key = anchor.Key
	input.Name = "Renamed"
	input.Headers = map[string]string{"x-access": ""}
	raw, err = ChangeConnection(context.Background(), raw, true, http.MethodPut, input)
	if err != nil || raw.Models[0].Headers["X-Access"] != "secret-header" {
		t.Fatalf("did not retain header: %v", err)
	}
	input.BaseURL = upstream.URL + "/other"
	if _, err = ChangeConnection(context.Background(), raw, true, http.MethodPut, input); err == nil {
		t.Error("reused header at a different address")
	}
	input.BaseURL = upstream.URL
	input.CredentialMode = "bearer"
	input.APIKey = "new-key"
	expectedAuth = "Bearer new-key"
	raw, err = ChangeConnection(context.Background(), raw, true, http.MethodPut, input)
	if err != nil || raw.Models[0].APIKey != "new-key" {
		t.Fatalf("key edit failed: %v", err)
	}
	input.CredentialMode = "none"
	input.APIKey = ""
	input.Headers = nil
	expectedAuth = ""
	expectedHeader = ""
	raw, err = ChangeConnection(context.Background(), raw, true, http.MethodPut, input)
	if err != nil || raw.Models[0].APIKey != "" || len(raw.Models[0].Headers) != 0 {
		t.Fatalf("anonymous mode/removal retained credentials: %v", err)
	}
	if _, err = Save(dir, raw); err != nil {
		t.Fatal(err)
	}
	loaded, err = Load(dir)
	if err != nil || loaded.Models[0].APIKey != "" || len(loaded.Models[0].Headers) != 0 {
		t.Fatalf("reload retained removed credentials: %v", err)
	}
}

func TestCustomDiscoveryRecoveryIncludesRestrictedModelLists(t *testing.T) {
	for _, status := range []int{401, 403, 404, 503, 200} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
				_, _ = w.Write([]byte(`{"data":[]}`))
			}))
			defer upstream.Close()
			_, err := ChangeConnection(context.Background(), Catalog{}, false, http.MethodPost, ConnectionInput{Provider: "openai-chat", ProviderID: "custom", BaseURL: upstream.URL, CredentialMode: "none"})
			var settings *SettingsError
			if !errors.As(err, &settings) || settings.Code != "model_ids_required" {
				t.Fatalf("no manual recovery: %v", err)
			}
			if status != 200 && !strings.Contains(settings.Message, "HTTP") {
				t.Fatalf("discovery failure lost its status: %v", err)
			}
		})
	}
}

func TestCustomDiscoveryRejectsRepeatedPagination(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"models":[{"name":"models/gemini-test"}],"nextPageToken":"repeat"}`))
	}))
	defer upstream.Close()
	_, err := DiscoverCustomModels(context.Background(), Configured{Provider: "gemini", ProviderID: "custom", Authentication: "none", BaseURL: upstream.URL + "/v1beta"})
	if err == nil || !strings.Contains(err.Error(), "repeated") {
		t.Fatalf("unbounded pagination: %v", err)
	}
}

func TestCohereDiscoveryVersionsAndCustomPrefixes(t *testing.T) {
	for _, tc := range []struct{ base, path string }{
		{"", "/v1/models"}, {"/v2", "/v1/models"},
		{"/proxy/v2", "/proxy/v1/models"}, {"/proxy/v1", "/proxy/v1/models"},
		{"/proxy", "/proxy/models"},
	} {
		t.Run(tc.base, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != tc.path || r.URL.Query().Get("endpoint") != "chat" || r.Header.Get("Authorization") != "Overridden token" {
					t.Error("Cohere discovery lost endpoint version, prefix, filter or authentication override")
				}
				_, _ = w.Write([]byte(`{"models":[{"name":"command","endpoints":["chat"]}]}`))
			}))
			defer upstream.Close()
			models, err := DiscoverCustomModels(context.Background(), Configured{Provider: "cohere", ProviderID: "custom", Authentication: "bearer", APIKey: "original-key", Headers: map[string]string{"Authorization": "Overridden token"}, BaseURL: upstream.URL + tc.base})
			if err != nil || len(models) != 1 || models[0].ID != "command" {
				t.Fatalf("Cohere models=%d error=%v", len(models), err)
			}
		})
	}
}
