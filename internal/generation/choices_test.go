package generation

import (
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"

	"vista/internal/model"
	"vista/internal/provider"
)

func TestDeclaredChoicesUseCatalogNamesOrModelIDs(t *testing.T) {
	original := provider.CurrentOMPCatalog()
	t.Cleanup(original.Activate)
	dir := t.TempDir()
	catalog := `{"source":"oh-my-pi","revision":"fixture-image-display-names","modelCount":3,"providers":{
		"openai":{
			"gpt-image-1.5":{"name":"gpt-image-1.5","api":"openai-images","input":["text"]},
			"gpt-image-2":{"name":"Upstream Image 2","api":"openai-responses","input":["text"]}
		},
		"openrouter":{"gpt-image-2":{"name":"gpt-image-2","api":"openrouter-images","input":["text"]}}
	}}`
	if err := os.WriteFile(provider.OMPCatalogPath(dir), []byte(catalog), 0600); err != nil {
		t.Fatal(err)
	}
	if err := provider.LoadOMPCatalog(dir); err != nil {
		t.Fatal(err)
	}
	choices := Choices("openai-codex", model.ImageOutput)
	if len(choices) != 2 || choices[0].ID != "gpt-image-1.5" || choices[0].Name != "gpt-image-1.5" || choices[1].ID != "gpt-image-2" || choices[1].Name != "Upstream Image 2" {
		t.Fatalf("Codex must reuse catalog names without inheriting OpenAI-only declarations: %+v", choices)
	}
	for _, adapter := range []string{"litellm", "openrouter"} {
		rows := Choices(adapter, model.ImageOutput)
		if i := slices.IndexFunc(rows, func(m provider.OutputMetadata) bool { return m.ID == "gpt-image-2" }); i < 0 || rows[i].Name != rows[i].ID {
			t.Errorf("%s must show the ID when its own catalog has no display name: %+v", adapter, rows)
		}
	}
}

func TestClawRouterDecisionUsesDiscoveredName(t *testing.T) {
	var catalog wire
	payload := strings.Replace(clawRouterCatalogFixture, `"upstream":"gpt-6-luna"`, `"upstream":"gpt-6-luna","displayName":"Gateway Luna"`, 1)
	dec := json.NewDecoder(strings.NewReader(payload))
	dec.UseNumber()
	if err := dec.Decode(&catalog); err != nil {
		t.Fatal(err)
	}
	rows := clawRouterChoices(catalog, "fixture-key")
	if !slices.ContainsFunc(rows, func(row provider.OutputMetadata) bool {
		return row.Kind == model.DecisionOutput && row.ID == "openai/gpt-6-luna" && row.Name == "Gateway Luna"
	}) {
		t.Fatalf("decision choice did not use the advertised name: %+v", rows)
	}
}
