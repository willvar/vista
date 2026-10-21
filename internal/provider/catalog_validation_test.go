package provider

import (
	"testing"

	modelcfg "vista/internal/model"
)

func TestCatalogValidationKeepsFamilyAndRouteRestrictions(t *testing.T) {
	for _, test := range []struct {
		name string
		item CatalogConfig
		want string
	}{
		{
			name: "direct Anthropic without service ID",
			item: CatalogConfig{Provider: "anthropic", Thinking: &modelcfg.ThinkingOption{Type: "adaptive"}},
		},
		{
			name: "Anthropic service on another protocol",
			item: CatalogConfig{Provider: "openai-responses", ProviderID: "anthropic", Thinking: &modelcfg.ThinkingOption{Type: "adaptive"}},
			want: `model "model" thinking requires a ZAI catalog model`,
		},
		{
			name: "ZAI coding plan Messages effort",
			item: CatalogConfig{Provider: "anthropic", ProviderID: "zai-coding-plan", Effort: "high"},
		},
		{
			name: "ordinary ZAI has no Messages effort",
			item: CatalogConfig{Provider: "anthropic", ProviderID: "zai", Effort: "high"},
			want: `model "model" has invalid Messages effort`,
		},
		{
			name: "MiniMax family permits thinking",
			item: CatalogConfig{Provider: "openai-chat", ProviderID: "minimax-other", Thinking: &modelcfg.ThinkingOption{Type: "adaptive"}},
		},
		{
			name: "MiniMax effort requires a known service",
			item: CatalogConfig{Provider: "openai-responses", ProviderID: "minimax-other", ReasoningEffort: "high"},
			want: `model "model" reasoningEffort is unsupported for this catalog Provider`,
		},
		{
			name: "Alibaba family permits thinking",
			item: CatalogConfig{Provider: "openai-chat", ProviderID: "alibaba-other", Thinking: &modelcfg.ThinkingOption{Type: "adaptive"}},
		},
		{
			name: "Alibaba effort requires a known service",
			item: CatalogConfig{Provider: "openai-responses", ProviderID: "alibaba-other", ReasoningEffort: "high"},
			want: `model "model" reasoningEffort is unsupported for this catalog Provider`,
		},
		{
			name: "thinking is checked before effort",
			item: CatalogConfig{Provider: "openai-chat", ProviderID: "minimax", Thinking: &modelcfg.ThinkingOption{Type: "invalid"}, Effort: "invalid", ReasoningEffort: "invalid"},
			want: `model "model" has invalid MiniMax thinking option`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			test.item.Key = "model"
			err := ValidateCatalogOptions(test.item, CatalogMetadata{})
			if test.want == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || err.Error() != test.want {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}
