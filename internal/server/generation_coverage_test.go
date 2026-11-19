package server

import (
	"slices"
	"testing"

	"vista/internal/model"
	"vista/internal/modelcatalog"
)

func TestConsoleDecisionsReuseSavedOpenCodeNotGo(t *testing.T) {
	base := modelCatalog{Models: []configuredModel{
		{Key: "console", ConnectionID: "console", Provider: "catalog", ProviderID: "opencode", Name: "Saved OpenCode", ID: "text-carrier", BaseURL: "https://opencode.ai/zen/v1", APIKey: "fixture-console", ProxyID: "saved-proxy"},
		{Key: "go", ConnectionID: "go", Provider: "catalog", ProviderID: "opencode-go", Name: "Saved Go", ID: "text-carrier", BaseURL: "https://opencode.ai/zen/go/v1", APIKey: "fixture-go"},
	}}
	choices := generationOptions(base, nil, model.DecisionOutput)
	for _, item := range choices {
		if item.Connection != "console" {
			t.Fatal("OpenCode Go offered a decision outside its route")
		}
	}
	if !slices.ContainsFunc(choices, func(row generationOption) bool {
		return row.Model == "jev-1.13" && row.Name == "Jev 1.13" && row.Connection == "console"
	}) {
		t.Fatalf("existing OpenCode Console cannot select Jev: %+v", choices)
	}
	configured, err := base.EditGeneration(modelcatalog.GenerationEdit{Kind: model.DecisionOutput, Connection: "console", Model: "jev-1.13"})
	if err != nil {
		t.Fatal(err)
	}
	bound := configured.GenerationTarget(configured.Models[len(configured.Models)-1])
	if bound.Error != "" || bound.Model.ProviderID != "opencode" || bound.Model.APIKey != "" || bound.Connection == nil || bound.Connection.APIKey != "fixture-console" || bound.Connection.ProxyID != "saved-proxy" {
		t.Fatal("binding did not preserve the existing Console connection")
	}
	if _, err = base.EditGeneration(modelcatalog.GenerationEdit{Kind: model.DecisionOutput, Connection: "go", Model: "jev-1.13"}); err == nil {
		t.Fatal("Go silently switched to Console")
	}
}
