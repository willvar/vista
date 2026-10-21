package modelcatalog

import (
	"testing"

	"vista/internal/provider"
)

func TestCustomModelLimitsFillMissingFieldsAndPreserveExplicitValues(t *testing.T) {
	want := provider.ModelLimitsFor("opencode", "gpt-6.1-sol")
	catalog := Catalog{Default: "custom", Models: []Configured{
		{Key: "custom", Provider: "openai-responses", ProviderID: "opencode", ID: "gpt-6.1-sol", BaseURL: "https://example.invalid/v1", APIKey: "test"},
		{Key: "explicit", Provider: "openai-responses", ProviderID: "opencode", ID: "gpt-6.1-sol", Context: 8192, inputLimit: 6144, outputLimit: 2048},
		{Key: "unknown", Provider: "openai-responses", ID: "invented-vista-model"},
	}}
	if err := ResolveCatalog(&catalog); err != nil {
		t.Fatal(err)
	}
	model, _, err := catalog.Resolve("custom")
	if err != nil || model.Context != want.Context || model.InputLimit != want.Input || model.OutputLimit != want.Output {
		t.Fatalf("custom model limits: %+v, %v; want %+v", model, err, want)
	}
	explicit := catalog.Models[1]
	if explicit.Context != 8192 || explicit.inputLimit != 6144 || explicit.outputLimit != 2048 {
		t.Fatalf("explicit limits were replaced: %+v", explicit)
	}
	unknown := catalog.Models[2]
	if unknown.Context != 0 || unknown.inputLimit != 0 || unknown.outputLimit != 0 {
		t.Fatal("unknown model was assigned invented limits")
	}
	for _, item := range catalog.Public() {
		if item.Key == "custom" && (item.Context != want.Context || item.InputLimit != want.Input || item.OutputLimit != want.Output) {
			t.Fatalf("public custom limits missing: %+v", item)
		}
	}
}
