package provider

import (
	"strings"
	"testing"

	modelcfg "vista/internal/model"
)

func TestModelSelectionEndpointIDRemovesCredentialsAndNormalizes(t *testing.T) {
	a := modelcfg.Model{Provider: "openai-responses", BaseURL: "HTTPS://User:Secret@EXAMPLE.com/v1/?api_key=secret#token", APIKey: "one"}
	b := modelcfg.Model{Provider: "openai-responses", BaseURL: "https://example.com/v1", APIKey: "two"}
	if ModelEndpointID(a) != ModelEndpointID(b) {
		t.Fatal("credentials or cosmetic URL differences changed endpoint identity")
	}
	b.BaseURL = "https://different.example/v1"
	if ModelEndpointID(a) == ModelEndpointID(b) {
		t.Fatal("different endpoints merged")
	}
	if strings.Contains(ModelEndpointID(a), "secret") {
		t.Fatal("endpoint identifier contains credentials")
	}
}

func TestModelReasoningEffortShowsOnlyNamedActualSetting(t *testing.T) {
	budget := 4096
	for _, test := range []struct {
		model modelcfg.Model
		want  string
	}{
		{modelcfg.Model{}, ""},
		{modelcfg.Model{ReasoningEffort: "none"}, "none"},
		{modelcfg.Model{ReasoningEffort: "max", ThinkingBudget: &budget}, "max"},
		{modelcfg.Model{Effort: "high"}, "high"},
		{modelcfg.Model{ThinkingLevel: "low"}, "low"},
		{modelcfg.Model{ThinkingBudget: &budget}, ""},
		{modelcfg.Model{OpenRouter: &modelcfg.OpenRouterOptions{Reasoning: &modelcfg.OpenRouterReasoning{Effort: "medium"}}}, "medium"},
	} {
		if got := ModelReasoningEffort(test.model); got != test.want {
			t.Fatalf("display effort = %q, want %q", got, test.want)
		}
	}
}
