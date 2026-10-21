package modelcatalog

import (
	"strings"
	"testing"

	"vista/internal/provider"
)

// Every built-in OMP text model must remain usable through configuration,
// including native service endpoints and an explicit loopback endpoint.
func TestEveryBuiltInOMPTextModelLoadsThroughServer(t *testing.T) {
	var failures []string
	failed := 0
	report := func(providerID, modelID, mode string, err error) {
		failed++
		if len(failures) < 20 {
			failures = append(failures, providerID+"/"+modelID+" ("+mode+"): "+err.Error())
		}
	}
	count := 0
	for _, setup := range ProviderSetups() {
		providerID := setup.ID
		if providerID == "custom" || LocalProvider(providerID) {
			continue
		}
		for _, modelID := range provider.CatalogTextModelIDs(providerID) {
			model := Configured{Key: "pinned", Provider: "catalog", ProviderID: providerID, ID: modelID, APIKey: "fixture"}
			switch providerID {
			case "amazon-bedrock":
				model.Region = "us-east-1"
			case "azure":
				model.ResourceName = "fixture"
			}
			if providerID == "google-vertex" {
				model.APIKey = ""
				model.AccessToken = "fixture"
				model.Project = "fixture"
				model.Location = "global"
			}
			for _, mode := range []string{"pinned default", "custom loopback"} {
				configured := model
				if mode == "custom loopback" {
					configured.BaseURL = "http://127.0.0.1:1234/v1"
				}
				catalog := Catalog{Default: "pinned", Models: []Configured{configured}}
				if err := ResolveCatalog(&catalog); err != nil {
					report(providerID, modelID, mode+" route", err)
					continue
				}
				if err := Validate(catalog); err != nil {
					report(providerID, modelID, mode+" validation", err)
					continue
				}
				if _, _, err := catalog.Resolve(""); err != nil {
					report(providerID, modelID, mode+" selection", err)
				}
			}
			count++
		}
	}
	if failed != 0 {
		t.Errorf("%d pinned model configurations failed; first cases:\n%s", failed, strings.Join(failures, "\n"))
	}
	if count == 0 {
		t.Fatal("empty built-in OMP inventory")
	}
}
