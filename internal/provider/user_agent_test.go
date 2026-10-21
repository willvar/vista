package provider

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"vista/internal/appdirs"
	"vista/internal/copilotauth"
	modelcfg "vista/internal/model"
	"vista/internal/useragent"
)

func configureTestUserAgent(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	switch runtime.GOOS {
	case "linux":
		t.Setenv("XDG_CONFIG_HOME", root)
	case "windows":
		t.Setenv("AppData", root)
	default:
		t.Skip("isolated config override requires Linux or Windows")
	}
	dir, err := appdirs.Config()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := useragent.Write(dir, useragent.Config{Preset: "custom", Custom: "ConfiguredClient/1.0"}); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestSharedUserAgentAcrossProtocolsAndCatalog(t *testing.T) {
	configureTestUserAgent(t)
	agents := make(chan string, 1)
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		agents <- r.UserAgent()
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"error":{"message":"header capture complete"}}`)
	}))
	defer endpoint.Close()
	for _, protocol := range []string{"openai-chat", "openai-responses", "anthropic", "gemini", "cohere", "mistral"} {
		t.Run(protocol, func(t *testing.T) {
			model := modelcfg.Model{Provider: protocol, ID: "test-model", BaseURL: endpoint.URL, APIKey: "test-key"}
			for _, override := range []string{"", "ProviderOverride/2.0"} {
				want := "ConfiguredClient/1.0"
				if override != "" {
					model.Headers = map[string]string{"user-agent": override}
					want = override
				}
				_, err := ModelTurn(context.Background(), model, []ChatMessage{{Role: "user", Content: "hello"}}, nil)
				if err == nil {
					t.Fatal("expected the capture endpoint's HTTP 400")
				}
				select {
				case got := <-agents:
					if got != want {
						t.Errorf("sent UA %q, want %q", got, want)
					}
				default:
					t.Fatalf("request never reached the endpoint: %v", err)
				}
			}
		})
	}
	_, err := fetchCatalogFile(context.Background(), endpoint.Client(), endpoint.URL, "fixture", 4096)
	if err == nil {
		t.Fatal("expected catalog endpoint HTTP 400")
	}
	if got := <-agents; got != "ConfiguredClient/1.0" {
		t.Fatalf("catalog UA %q", got)
	}
}

func TestAccountChannelsAndAWSKeepNativeIdentity(t *testing.T) {
	dir := configureTestUserAgent(t)
	headers := make(chan http.Header, 1)
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		headers <- r.Header.Clone()
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"error":{"message":"header capture complete"}}`)
	}))
	defer endpoint.Close()
	models := []modelcfg.Model{
		{Provider: "openai-responses", ProviderID: "openai-codex", ResponsesRoute: modelcfg.ResponsesRouteCodex},
		{Provider: "openai-chat", ProviderID: "github-copilot"},
		{Provider: "openai-responses", ProviderID: "github-copilot"},
		{Provider: "anthropic", ProviderID: "github-copilot"},
		{Provider: "bedrock", AWSRegion: "us-east-1", AWSAccessID: "test-access", AWSSecret: "test-secret"},
		{Provider: "bedrock", AWSRegion: "us-east-1", AWSAccessID: "test-access", AWSSecret: "test-secret", Headers: map[string]string{"user-agent": "ConnectionOverride/1"}},
	}
	for _, brokenSettings := range []bool{false, true} {
		if brokenSettings {
			if err := os.WriteFile(filepath.Join(dir, useragent.Filename), []byte(`{"preset":`), 0600); err != nil {
				t.Fatal(err)
			}
		}
		for _, model := range models {
			model.ID, model.BaseURL, model.APIKey = "test-model", endpoint.URL, "test-key"
			if model.Provider == "bedrock" {
				model.APIKey = ""
			}
			t.Run(model.ProviderID+"/"+model.Provider+"/broken="+strconv.FormatBool(brokenSettings), func(t *testing.T) {
				_, err := ModelTurn(context.Background(), model, []ChatMessage{{Role: "user", Content: "hello"}}, nil)
				if err == nil {
					t.Fatal("expected capture endpoint's HTTP 400")
				}
				var got http.Header
				select {
				case got = <-headers:
				default:
					t.Fatalf("native request failed before dispatch: %v", err)
				}
				switch model.ProviderID {
				case "openai-codex":
					if got.Get("User-Agent") != useragent.Codex() || got.Get("originator") != useragent.CodexOriginator || got.Get("version") != useragent.CodexVersion {
						t.Errorf("inconsistent Codex identity: %v", got)
					}
				case "github-copilot":
					if got.Get("User-Agent") != copilotauth.UserAgent || got.Get("Editor-Version") != copilotauth.UserAgent || got.Get("Copilot-Integration-Id") != "copilot-developer-cli" || got.Get("Copilot-Harness-Id") != "copilot-sdk" {
						t.Errorf("inconsistent Copilot identity: %v", got)
					}
				default:
					if explicit := model.Headers["user-agent"]; explicit != "" {
						if got.Get("User-Agent") != explicit {
							t.Errorf("explicit connection UA lost: %q", got.Get("User-Agent"))
						}
					} else if !strings.HasPrefix(got.Get("User-Agent"), "aws-sdk-go-v2/") {
						t.Errorf("AWS SDK identity replaced: %s", got.Get("User-Agent"))
					}
				}
			})
		}
	}
}
