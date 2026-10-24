package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	modelcfg "vista/internal/model"
)

func TestSessionReasoningReachesProviderPayload(t *testing.T) {
	useExplicitReasoningCatalog(t)
	cases := []struct {
		service, protocol, id, effort string
		expected                      string
	}{
		{"anthropic", "anthropic", "claude-haiku-4-5", "budget:16000", `{"thinking":{"type":"enabled","budget_tokens":16000}}`},
		{"anthropic", "anthropic", "claude-fable-5", "max", `{"thinking":{"type":"adaptive"},"output_config":{"effort":"max"}}`},
		{"anthropic", "anthropic", "claude-opus-4-5", "high", `{"thinking":{"type":"enabled","budget_tokens":16000},"output_config":{"effort":"high"}}`},
		{"google", "gemini", "gemini-2.5-flash", "budget:16384", `{"generationConfig":{"thinkingConfig":{"thinkingBudget":16384}}}`},
		{"google", "gemini", "gemini-flash-latest", "high", `{"generationConfig":{"thinkingConfig":{"thinkingLevel":"HIGH"}}}`},
		{"google-vertex", "vertex-gemini", "gemini-3-flash-preview", "high", `{"generationConfig":{"thinkingConfig":{"thinkingLevel":"HIGH"}}}`},
		{"custom", "gemini", "gemini-flash-latest", "high", `{"generationConfig":{"thinkingConfig":{"thinkingLevel":"HIGH"}}}`},
		{"google", "gemini", "models/gemini-2.5-flash", "budget:16384", `{"generationConfig":{"thinkingConfig":{"thinkingBudget":16384}}}`},
		{"google", "gemini", "gemini-3-flash-preview", "high", `{"generationConfig":{"thinkingConfig":{"thinkingLevel":"HIGH"}}}`},
		{"openrouter", "openai-chat", "openai/gpt-6-luna", "high", `{"reasoning":{"effort":"high"}}`},
		{"deepseek", "openai-chat", "deepseek-flash", "max", `{"thinking":{"type":"enabled"},"reasoning_effort":"max"}`},
		{"minimax", "openai-chat", "MiniMax-M3", "on", `{"thinking":{"type":"adaptive"}}`},
		{"amazon-bedrock", "bedrock", "anthropic.claude-opus-4-6-v1", "max", `{"additionalModelRequestFields":{"thinking":{"type":"adaptive"},"output_config":{"effort":"max"}}}`},
		{"amazon-bedrock", "bedrock", "global.openai.gpt-5.6-luna", "xhigh", `{"additionalModelRequestFields":{"reasoning":{"effort":"xhigh"}}}`},
	}
	for _, tc := range cases {
		t.Run(tc.service+"/"+tc.id, func(t *testing.T) {
			var captured map[string]any
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
					t.Error(err)
				}
				http.Error(w, "fixture ends after request encoding", 400)
			}))
			defer upstream.Close()
			model := modelcfg.Model{Provider: tc.protocol, ProviderID: tc.service, ID: tc.id, APIKey: "fixture", BaseURL: upstream.URL, AWSRegion: "us-east-1", OutputLimit: 65536}
			if tc.protocol == "bedrock" {
				model.APIKey = ""
				model.AWSAccessID = "fixture-id"
				model.AWSSecret = "fixture-secret"
			}
			if err := ApplyReasoningEffort(&model, tc.effort); err != nil {
				t.Fatal(err)
			}
			_, err := ModelTurn(context.Background(), model, []ChatMessage{{Role: "user", Content: "hello"}}, nil)
			if captured == nil {
				t.Fatalf("request was rejected before HTTP: %v", err)
			}
			var expected map[string]any
			_ = json.Unmarshal([]byte(tc.expected), &expected)
			assertRequestSubset(t, captured, expected)
			model.ReasoningDisabled = true
			captured = nil
			_, _ = ModelTurn(context.Background(), model, []ChatMessage{{Role: "user", Content: "title"}}, nil)
			if captured == nil {
				t.Fatal("title request was rejected")
			}
			for _, field := range []string{"thinking", "reasoning_effort", "reasoning", "output_config", "additionalModelRequestFields", "generationConfig"} {
				if _, ok := captured[field]; ok {
					t.Fatalf("lightweight request included reasoning %s", field)
				}
			}
		})
	}
}

func assertRequestSubset(t *testing.T, got, want map[string]any) {
	t.Helper()
	for key, value := range want {
		if object, ok := value.(map[string]any); ok {
			actual, ok := got[key].(map[string]any)
			if !ok {
				t.Fatalf("missing object %s in %v", key, got)
			}
			assertRequestSubset(t, actual, object)
		} else if got[key] != value {
			t.Fatalf("%s=%v, want %v", key, got[key], value)
		}
	}
}

func TestUnsupportedReasoningControlsUseProviderDefaults(t *testing.T) {
	for _, model := range []modelcfg.Model{
		{Provider: "openai-chat", ProviderID: "zai", ID: "glm-4.5"},
		{Provider: "mistral", ProviderID: "mistral", ID: "magistral-medium-latest"},
		{Provider: "gemini", ProviderID: "google", ID: "gemini-2.0-flash"},
	} {
		if len(SessionReasoningOptions(model)) != 0 {
			t.Fatalf("offered unsupported controls for %s", model.ID)
		}
		if err := ApplyReasoningEffort(&model, ""); err != nil {
			t.Fatal(err)
		}
	}
}
