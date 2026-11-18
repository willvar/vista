package modelcatalog

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"vista/internal/generation"
	"vista/internal/model"
	"vista/internal/provider"
)

const generationProvider = "generation"

// Omit secrets from public settings; only connection-local owner metadata is
// needed to restore the form. Catalog metadata supplies suggestions, not auth.
type GenerationSetting struct {
	Kind        string          `json:"kind"`
	Adapter     string          `json:"adapter"`
	Model       string          `json:"model"`
	Connection  string          `json:"connection,omitempty"`
	BaseURL     string          `json:"baseUrl,omitempty"`
	ProxyID     string          `json:"proxyId,omitempty"`
	HasAPIKey   bool            `json:"hasApiKey,omitempty"`
	Options     json.RawMessage `json:"options,omitempty"`
	HasWorkflow bool            `json:"hasWorkflow,omitempty"`
}

type GenerationEdit struct {
	Kind       string          `json:"kind"`
	Adapter    string          `json:"adapter"`
	Model      string          `json:"model"`
	Connection string          `json:"connection,omitempty"`
	BaseURL    string          `json:"baseUrl,omitempty"`
	ProxyID    string          `json:"proxyId,omitempty"`
	APIKey     *string         `json:"apiKey,omitempty"`
	Options    json.RawMessage `json:"options,omitempty"`
}

func (c Catalog) GenerationSettings() []GenerationSetting {
	items := []GenerationSetting{}
	for _, m := range c.Models {
		if m.Generation != nil {
			items = append(items, GenerationSetting{Kind: m.Generation.Kind, Adapter: m.ProviderID, Model: m.ID, Connection: m.Generation.Connection, BaseURL: m.BaseURL, ProxyID: m.ProxyID, HasAPIKey: m.APIKey != "", Options: publicWorkflowOptions(m.Generation.Options), HasWorkflow: len(m.Generation.Options) != 0})
		}
	}
	return items
}

func (c Catalog) EditGeneration(edit GenerationEdit) (Catalog, error) {
	if edit.Kind != model.ImageOutput && edit.Kind != model.VideoOutput && edit.Kind != model.DecisionOutput {
		return c, errors.New("invalid generation purpose")
	}
	out := c
	out.Models = nil
	var prior Configured
	for _, row := range c.Models {
		if row.Generation != nil && row.Generation.Kind == edit.Kind {
			prior = row
			continue
		}
		out.Models = append(out.Models, row)
	}
	if edit.Adapter == "" && edit.Model == "" {
		return out, nil
	}
	spec, ok := generation.Find(edit.Adapter, edit.Kind)
	if !ok || !generation.ValidateModelID(edit.Model) {
		return c, errors.New("choose a valid model for this operation")
	}
	if k := provider.DeclaredModelKind(edit.Adapter, edit.Model); k != "" && k != edit.Kind && !(k == "chat" && edit.Adapter == "openai-codex") {
		return c, errors.New("the selected model has a different output category")
	}
	base := strings.TrimSpace(edit.BaseURL)
	if edit.Connection == "" && base == "" {
		base = spec.Base
	}
	row := Configured{Key: "generation-" + edit.Kind, Provider: generationProvider, ProviderID: edit.Adapter, ID: edit.Model, BaseURL: base, ProxyID: edit.ProxyID, Generation: &model.GenerationBinding{Kind: edit.Kind, Connection: edit.Connection, Options: edit.Options}}
	if edit.Connection == "" {
		if edit.APIKey != nil {
			row.APIKey = *edit.APIKey
		} else if prior.ProviderID == row.ProviderID && prior.BaseURL == row.BaseURL && prior.Generation.Connection == "" {
			row.APIKey = prior.APIKey
		}
	} else {
		match := false
		for _, src := range c.Models {
			if src.Key == edit.Connection && src.Generation == nil && src.Available() && matchingGenerationProvider(src.ProviderID, edit.Adapter) {
				match = true
				break
			}
		}
		if !match {
			return c, errors.New("the selected connection cannot serve this adapter")
		}
		if base != "" || edit.ProxyID != "" || edit.APIKey != nil {
			return c, errors.New("reuse the selected connection's address, network and credentials")
		}
	}
	if edit.Adapter == "comfy" && prior.Generation != nil && prior.ProviderID == row.ProviderID && prior.BaseURL == row.BaseURL && prior.Generation.Connection == row.Generation.Connection {
		var previous, next map[string]json.RawMessage
		_ = json.Unmarshal(prior.Generation.Options, &previous)
		if json.Unmarshal(edit.Options, &next) == nil && next != nil && next["workflow"] == nil && previous["workflow"] != nil {
			next["workflow"] = previous["workflow"]
			row.Generation.Options, _ = json.Marshal(next)
		}
	}
	if err := validateGeneration(row); err != nil {
		return c, err
	}
	if slices.ContainsFunc(out.Models, func(m Configured) bool { return m.Key == row.Key }) {
		return c, errors.New("generation setting key conflicts with an existing model key")
	}
	out.Models = append(out.Models, row)
	return out, nil
}

func matchingGenerationProvider(id, adapter string) bool {
	return id == adapter || id == "togetherai" && adapter == "together" || id == "minimax" && adapter == "minimax" || id == "azure" && adapter == "microsoft-foundry"
}

func validateGeneration(m Configured) error {
	spec, ok := generation.Find(m.ProviderID, m.Generation.Kind)
	if m.Provider != generationProvider || !ok || !generation.ValidateModelID(m.ID) || len(m.Generation.Connection) > 64 || len(m.APIKey) > 4096 || len(m.Generation.Options) > 20<<10 {
		return errors.New("invalid generation binding")
	}
	if m.Generation.Connection == "" {
		if _, err := generation.Endpoint(m.BaseURL); err != nil {
			return err
		}
		if spec.ID == "openai-codex" {
			return errors.New("select an existing Codex connection")
		}
	} else if m.BaseURL != "" || m.APIKey != "" || m.ProxyID != "" {
		return errors.New("a linked generation route cannot override its connection")
	}
	if spec.ID == "comfy" {
		var opt struct {
			Workflow   json.RawMessage `json:"workflow"`
			PromptNode string          `json:"promptNode"`
			OutputNode string          `json:"outputNode"`
		}
		if json.Unmarshal(m.Generation.Options, &opt) != nil || len(opt.Workflow) == 0 || opt.Workflow[0] != '{' || opt.PromptNode == "" || opt.OutputNode == "" {
			return errors.New("select a ComfyUI API workflow with prompt and output nodes")
		}
	} else if len(m.Generation.Options) != 0 {
		return errors.New("generation options are only accepted for ComfyUI")
	}

	return nil
}

func (c Catalog) GenerationTarget(row Configured) (model.GenerationTarget, string) {
	target := model.GenerationTarget{Kind: row.Generation.Kind, Options: row.Generation.Options, Model: model.Model{ProviderID: row.ProviderID, ID: row.ID, BaseURL: row.BaseURL, APIKey: row.APIKey, ProxyID: row.ProxyID}}
	if row.Generation.Connection != "" {
		for _, source := range c.Models {
			if source.Key != row.Generation.Connection || source.Generation != nil || !matchingGenerationProvider(source.ProviderID, row.ProviderID) {
				continue
			}
			if source.Available() {
				return target, source.Key
			}
			break
		}
		target.Error = "The configured generation connection is unavailable. Check model settings."
	}
	return target, ""
}

func publicWorkflowOptions(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	var options map[string]json.RawMessage
	if json.Unmarshal(raw, &options) != nil {
		return nil
	}
	out := map[string]json.RawMessage{}
	for _, field := range []string{"promptNode", "promptField", "imageNode", "imageField", "outputNode", "cloud"} {
		if value, ok := options[field]; ok {
			out[field] = value
		}
	}
	result, _ := json.Marshal(out)
	return result
}
