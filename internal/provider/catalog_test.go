package provider

import (
	"slices"
	"testing"
)

func TestZenReasoningEffortsFollowCatalogSourcePriority(t *testing.T) {
	useExplicitReasoningCatalog(t)
	tests := []struct {
		provider, model string
		want            []string
	}{
		{"opencode", "gpt-5.4", []string{"none", "low", "medium", "high", "xhigh"}},
		{"opencode", "claude-sonnet-4-6", []string{"low", "medium", "high", "max"}},
		{"opencode", "gemini-3-flash", []string{"minimal", "low", "medium", "high"}},
		{"opencode", "deepseek-v4-flash", []string{"low", "high", "max"}},
		{"opencode", "big-pickle", []string{"high", "max"}},
	}
	for _, check := range tests {
		entry, err := ResolveCatalogModel(check.provider, check.model)
		if err != nil || !slices.Equal(entry.ReasoningEfforts, check.want) {
			t.Fatalf("%s/%s efforts: %v, error: %v", check.provider, check.model, entry.ReasoningEfforts, err)
		}
	}
}

// Every selectable model must resolve from OMP metadata and a Vista route.
func TestEveryOMPTextModelHasAProtocolRoute(t *testing.T) {
	if _, err := LookupCatalogProvider("openai"); err != nil {
		t.Fatal(err)
	}
	count := 0
	for providerID := range catalogProviders {
		for _, id := range CatalogTextModelIDs(providerID) {
			metadata, ok := OMPModelMetadata(providerID, id)
			entry, err := ResolveCatalogModel(providerID, id)
			if !ok || err != nil || entry.Protocol == "" || entry.Name != metadata.Name || entry.ContextLimit != metadata.ContextWindow || entry.OutputLimit != metadata.MaxTokens || entry.InputLimit != 0 || entry.Reasoning != metadata.Reasoning {
				t.Fatalf("OMP model %s/%s: %+v, metadata=%+v, err=%v", providerID, id, entry, metadata, err)
			}
			count++
		}
	}
	if count == 0 {
		t.Fatal("empty OMP route inventory")
	}
}

func TestOMPCatalogPreservesNativeRoutes(t *testing.T) {
	for _, check := range []struct{ provider, id, protocol, base string }{
		{"openai", "gpt-4.1-mini", "openai-responses", ""},
		{"mistral", "mistral-small-latest", "mistral", ""},
		{"deepseek", "deepseek-flash", "openai-chat", "https://api.deepseek.com/v1"},
		{"google-vertex", "gemini-2.5-flash", "vertex-gemini", ""},
		{"google-vertex", "claude-haiku-4-5@20251001", "vertex-anthropic", ""},
		{"google-vertex", "xai/grok-4.6", "openai-chat", ""},
		{"amazon-bedrock", "anthropic.claude-3-5-haiku-20241022-v1:0", "bedrock", ""},
		{"amazon-bedrock", "openai.gpt-5.5", "bedrock-mantle-responses", "https://bedrock-mantle.${AWS_REGION}.api.aws/openai/v1"},
		{"azure", "gpt-4.1-mini", "openai-responses", ""},
		{"github-copilot", "claude-sonnet-4.6", "anthropic", "https://api.githubcopilot.com"},
		{"github-copilot", "gpt-5-mini", "openai-chat", "https://api.githubcopilot.com"},
		{"github-copilot", "gpt-6.1-sol", "openai-responses", "https://api.githubcopilot.com"},
	} {
		t.Run(check.provider+"/"+check.id, func(t *testing.T) {
			entry, err := ResolveCatalogModel(check.provider, check.id)
			if err != nil || entry.Protocol != check.protocol || check.base != "" && entry.BaseURL != check.base {
				t.Fatalf("native model route: %+v, err=%v", entry, err)
			}
		})
	}
}

func TestOMPCatalogHasNoVistaMetadataFallback(t *testing.T) {
	for _, check := range []struct{ provider, id string }{
		{"cohere", "command-a-03-2025"},
		{"opencode-go", "omen-alpha"},
		{"openai", "not-an-omp-model"},
		{"does-not-exist", "gpt-4.1-mini"},
	} {
		if _, err := ResolveCatalogModel(check.provider, check.id); err == nil {
			t.Fatalf("model absent from OMP was accepted: %s/%s", check.provider, check.id)
		}
		if name := ModelDisplayNameFor(check.provider, check.id); name != "" {
			t.Fatalf("model absent from OMP borrowed a label: %q", name)
		}
	}
}

func TestOMPCatalogExcludesNonConversationModels(t *testing.T) {
	for _, id := range []string{"chatgpt-image-latest", "gpt-image-1.5", "gpt-image-2", "text-embedding-3-small", "whisper-1"} {
		if slices.Contains(CatalogTextModelIDs("openai"), id) {
			t.Fatalf("non-conversation model is selectable: %s", id)
		}
		if _, err := ResolveCatalogModel("openai", id); err == nil {
			t.Fatalf("non-conversation model has a conversation route: %s", id)
		}
	}
	if !ompTextModel(OMPModel{API: "openai-completions", Input: []string{"text"}}) {
		t.Fatal("unknown limits were treated as no text support")
	}
}
