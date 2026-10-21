package provider

import (
	"slices"
	"testing"
)

func TestOMPCatalogSnapshotIsEmbeddedAndProviderAware(t *testing.T) {
	if revision := OMPCatalogRevision(); revision != "602b6c812fa9ef774f359f1e399a09d30ee2eaca" {
		t.Fatalf("OMP snapshot revision: %q", revision)
	}
	model, ok := OMPModelMetadata("opencode", "gpt-6.1-sol")
	if !ok || model.Name != "GPT-6.1 Sol" || model.API != "openai-responses" || model.ContextWindow != 1050000 {
		t.Fatalf("OpenCode alias metadata: %+v, ok=%v", model, ok)
	}
	if got := ModelReasoningOptionsFor("opencode", "gpt-6.1-sol"); !slices.Equal(got, []string{"low", "medium", "high", "xhigh", "max"}) {
		t.Fatalf("OMP effort ladder: %v", got)
	}
	model.Input[0] = "mutated"
	again, _ := OMPModelMetadata("opencode", "gpt-6.1-sol")
	if again.Input[0] == "mutated" {
		t.Fatal("OMP metadata returned shared input slice")
	}
	unknown, ok := OMPModelMetadata("opencode", "big-pickle")
	if !ok {
		t.Fatalf("missing OMP model metadata: %+v, ok=%v", unknown, ok)
	}
}

func TestOMPCatalogSuppliesZenModelsWithVistaRoutes(t *testing.T) {
	responses, err := ResolveCatalogModel("opencode", "gpt-6.1-sol")
	if err != nil || responses.Protocol != "openai-responses" || responses.BaseURL != "https://opencode.ai/zen/v1" || responses.ContextLimit != 1050000 {
		t.Fatalf("OMP-only Responses route: %+v, err=%v", responses, err)
	}
	entry, err := ResolveCatalogModel("opencode-go", "longcat-2.5-preview-free")
	if err != nil {
		t.Fatal(err)
	}
	if entry.Protocol != "openai-chat" || entry.BaseURL != "https://opencode.ai/zen/go/v1" || entry.Status != "active" || entry.ContextLimit != 1000000 {
		t.Fatalf("OMP-only Zen route: %+v", entry)
	}
	if options := ModelReasoningOptionsFor("opencode-go", "longcat-2.5-preview-free"); len(options) != 0 {
		t.Fatalf("dial-less OMP model exposed an effort ladder: %v", options)
	}
	if _, err := ResolveCatalogModel("opencode-go", "retired-model"); err == nil {
		t.Fatal("unlisted retired model was accepted")
	}
}

func TestOMPModelIDsAreSortedAndAliasPaidZen(t *testing.T) {
	ids := OMPModelIDs("opencode")
	if len(ids) == 0 || !slices.IsSorted(ids) {
		t.Fatalf("OMP Zen IDs are not sorted: %v", ids)
	}
	if !slices.Contains(ids, "gpt-6.1-sol") {
		t.Fatalf("OMP Zen IDs omit gpt-6.1-sol: %v", ids)
	}
}

func TestProviderlessMetadataUsesOneDeterministicOMPRow(t *testing.T) {
	if got := ModelReasoningOptionsFor("", "gpt-6-luna"); !slices.Equal(got, []string{"low", "medium", "high", "xhigh", "max"}) {
		t.Fatalf("providerless OMP effort ladder: %v", got)
	}
	limits := ModelLimitsFor("", "gpt-6-luna")
	if limits.Context != 1050000 || limits.Output != 128000 {
		t.Fatalf("providerless OMP limits: %+v", limits)
	}
	if got := ModelDisplayNameFor("", "gpt-6-luna"); got != "GPT-6 Luna" {
		t.Fatalf("providerless OMP name: %q", got)
	}
	if got := ModelInputModalitiesFor("", "gpt-6-luna"); !slices.Equal(got, []string{"text", "image"}) {
		t.Fatalf("providerless OMP modalities: %v", got)
	}
	if got := ModelReasoningOptionsFor("", "vista-private-model"); len(got) != 0 || ModelDisplayNameFor("", "vista-private-model") != "" {
		t.Fatalf("unknown providerless model received guessed metadata: efforts=%v name=%q", got, ModelDisplayNameFor("", "vista-private-model"))
	}
}
