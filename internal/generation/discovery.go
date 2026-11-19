package generation

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"unicode/utf8"

	"vista/internal/model"
	"vista/internal/provider"
)

// These services publish an account- or installation-specific inventory, not
// credential-independent choices. Read via the same saved connection client.
func NeedsInventory(adapter string) bool { return adapter == "clawrouter" || adapter == "onnx" }

func DiscoverInventory(ctx context.Context, source model.Model) ([]provider.OutputMetadata, error) {
	if !NeedsInventory(source.ProviderID) {
		return nil, errors.New("the service does not have a generation discovery endpoint")
	}
	c, err := newRequest(ctx, model.GenerationTarget{Kind: model.DecisionOutput,
		Model: model.Model{ProviderID: source.ProviderID, ID: "inventory"}, Connection: &source})
	if err != nil {
		return nil, err
	}
	if source.ProviderID == "clawrouter" {
		catalog, err := c.api(http.MethodGet, c.route("catalog"), nil, nil)
		if err != nil {
			return nil, err
		}
		return clawRouterChoices(catalog, c.key), nil
	}
	catalog, err := c.api(http.MethodGet, c.route("models"), nil, nil)
	if err != nil {
		return nil, err
	}
	models, ok := catalog["data"].([]any)
	if !ok {
		return nil, errors.New("the local ONNX service did not return models")
	}
	var rows []provider.OutputMetadata
	for _, m := range models {
		id := text(m, "id")
		if text(m, "type") == model.DecisionOutput && ValidateModelID(id) {
			name := text(m, "name")
			if len(name) > 300 {
				name = id
			}
			rows = append(rows, provider.OutputMetadata{ID: id, Name: name, Kind: model.DecisionOutput, Input: []string{"text"}})
		}
	}
	return rows, nil
}

func clawRouterImagePromptLimit(m any) int {
	if text(m, "pricing", "unit") != "image_token" {
		return 0
	}
	limits := map[string]int64{}
	for _, field := range []string{"maxPromptCharacters", "maxImages", "dimensionMultiple", "minPixels", "maxPixels", "maxEdge", "maxAspectRatio"} {
		num, ok := getValue(m, "pricing", "generation", field).(json.Number)
		if !ok {
			return 0
		}
		v, err := num.Int64()
		if err != nil || v < 1 || v > 1<<53-1 {
			return 0
		}
		limits[field] = v
	}
	if limits["minPixels"] > limits["maxPixels"] {
		return 0
	}
	return int(min(limits["maxPromptCharacters"], 32000))
}

func clawRouterChoices(catalog wire, key string) []provider.OutputMetadata {
	providers, _ := catalog["providers"].([]any)
	var rows []provider.OutputMetadata
	for _, p := range providers {
		if text(p, "id") != "openai" {
			continue
		}
		models, _ := getValue(p, "models").([]any)
		routes, _ := getValue(p, "routes").([]any)
		native := text(p, "nativeBaseUrl") == "/v1/native/openai" && slices.ContainsFunc(routes, func(r any) bool {
			methods, _ := getValue(r, "methods").([]any)
			return text(r, "path") == "/v1/decisions" && text(r, "requestFormat") == "openai.decisions" && slices.Contains(methods, any("POST"))
		})
		offers, _ := getValue(p, "offers").([]any)
		for _, m := range models {
			id := text(m, "id")
			capabilities, _ := getValue(m, "capabilities").([]any)
			name := text(m, "displayName")
			if len(name) > 300 || key != "" && strings.Contains(name, key) {
				name = ""
			}
			if !ValidateModelID(id) || key != "" && strings.Contains(id, key) {
				continue
			}
			if native && id == "openai/gpt-6-luna" && text(m, "upstream") == "gpt-6-luna" && slices.Contains(capabilities, any("llm.decisions")) {
				rows = append(rows, provider.OutputMetadata{ID: id, Name: "GPT-6 Luna", Kind: model.DecisionOutput, Input: []string{"text"}})
			}
			if text(catalog, "version") != "clawrouter.client-catalog.v1" || getValue(p, "allowed") != true || getValue(p, "executable") != true || !slices.Contains(capabilities, any("image.generation")) || clawRouterImagePromptLimit(m) == 0 {
				continue
			}
			switch id {
			case "openai/gpt-image-2.5-flare", "openai/gpt-image-2.5-flare-2026-09-08", "openai/gpt-image-2.5-sunburst", "openai/gpt-image-2.5-sunburst-2026-09-08":
			default:
				continue
			}
			if slices.ContainsFunc(offers, func(o any) bool {
				fee := text(o, "affordability")
				return text(o, "endpoint") == "images_generations" && text(o, "modelId") == id && text(o, "transport") == "http" && text(o, "routeKind") == "unified" && text(o, "route") == "/v1/images/generations" && getValue(o, "eligible") == true && (fee == "exact-covered" || fee == "request-dependent")
			}) {
				rows = append(rows, provider.OutputMetadata{ID: id, Name: name, Kind: model.ImageOutput, Input: []string{"text"}})
			}
		}
	}
	return rows
}

// Recheck route eligibility on the execution's saved key, without trusting
// transport URLs or authorizations obtained under another account.
func (c request) clawRouterAllows(kind string, prompt string) (bool, error) {
	out, err := c.api(http.MethodGet, c.route("catalog"), nil, nil)
	if err != nil {
		return false, err
	}
	eligible := slices.ContainsFunc(clawRouterChoices(out, c.key), func(m provider.OutputMetadata) bool { return m.ID == c.model && m.Kind == kind })
	if eligible && kind == model.ImageOutput {
		providers, _ := out["providers"].([]any)
		for _, p := range providers {
			if text(p, "id") != "openai" || getValue(p, "allowed") != true || getValue(p, "executable") != true {
				continue
			}
			rows, _ := getValue(p, "models").([]any)
			for _, row := range rows {
				if text(row, "id") == c.model && utf8.RuneCountInString(prompt) <= clawRouterImagePromptLimit(row) {
					return true, nil
				}
			}
		}
		return false, errors.New("description exceeds the ClawRouter image model's current limits")
	}
	return eligible, nil
}
