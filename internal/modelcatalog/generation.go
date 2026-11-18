package modelcatalog

import (
	"encoding/json"
	"errors"
	"slices"

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
	Options     json.RawMessage `json:"options,omitempty"`
	HasWorkflow bool            `json:"hasWorkflow,omitempty"`
}

type GenerationEdit struct {
	Kind       string          `json:"kind"`
	Adapter    string          `json:"adapter"`
	Model      string          `json:"model"`
	Connection string          `json:"connection,omitempty"`
	Options    json.RawMessage `json:"options,omitempty"`
}

func (c Catalog) GenerationSettings() []GenerationSetting {
	items := []GenerationSetting{}
	for _, m := range c.Models {
		if m.Generation != nil {
			ref := m.Generation.Connection
			if source, ok := c.generationConnection(ref, m.ProviderID, m.Generation.Kind, true); ok {
				ref = source.GenerationConnectionRef()
			}
			items = append(items, GenerationSetting{Kind: m.Generation.Kind, Adapter: m.ProviderID, Model: m.ID, Connection: ref, Options: publicWorkflowOptions(m.Generation.Options), HasWorkflow: len(m.Generation.Options) != 0})
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
	if edit.Adapter == "" && edit.Model == "" && edit.Connection == "" && len(edit.Options) == 0 {
		return out, nil
	}
	if !generation.ValidateModelID(edit.Model) {
		return c, errors.New("choose a valid model for this operation")
	}
	source, ok := c.generationConnection(edit.Connection, edit.Adapter, edit.Kind, false)
	if !ok {
		return c, errors.New("choose a saved, enabled connection and a compatible API format")
	}
	adapter := edit.Adapter
	if adapter == "" {
		adapter = source.GenerationAdapters(edit.Kind)[0]
	}
	if k := provider.DeclaredModelKind(generation.CatalogProviderID(adapter), edit.Model); k != "" && k != edit.Kind && !(k == "chat" && adapter == "openai-codex") {
		return c, errors.New("the selected model has a different output category")
	}
	row := Configured{Key: "generation-" + edit.Kind, Provider: generationProvider, ProviderID: adapter, ID: edit.Model, Generation: &model.GenerationBinding{Kind: edit.Kind, Connection: source.GenerationConnectionRef(), Options: edit.Options}}
	priorSource, priorOK := Configured{}, false
	if prior.Generation != nil {
		priorSource, priorOK = c.generationConnection(prior.Generation.Connection, prior.ProviderID, edit.Kind, true)
	}
	if adapter == "comfy" && priorOK && priorSource.GenerationConnectionRef() == source.GenerationConnectionRef() {
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

func (c Catalog) generationConnection(ref, adapter, kind string, allowDisabled bool) (Configured, bool) {
	if ref == "" {
		return Configured{}, false
	}
	// Prefer a stable connection identity, but accept old binding anchor keys.
	for _, byID := range []bool{true, false} {
		for _, row := range c.Models {
			if row.Generation != nil || (byID && row.ConnectionID != ref) || (!byID && row.Key != ref) {
				continue
			}
			if row.Disabled && !allowDisabled {
				return Configured{}, false
			}
			adapters := row.GenerationAdapters(kind)
			if (adapter == "" && len(adapters) == 1) || (adapter != "" && slices.Contains(adapters, adapter)) {
				return row, true
			}
			return Configured{}, false
		}
	}
	return Configured{}, false
}

func validateGeneration(m Configured) error {
	_, ok := generation.Find(m.ProviderID, m.Generation.Kind)
	if m.Provider != generationProvider || !ok || !generation.ValidateModelID(m.ID) || m.Generation.Connection == "" || len(m.Generation.Connection) > 64 || len(m.Generation.Options) > 20<<10 {
		return errors.New("invalid generation binding")
	}
	if m.BaseURL != "" || m.APIKey != "" || m.ProxyID != "" || len(m.Headers) != 0 || m.AccessToken != "" || m.AuthToken != "" {
		return errors.New("a specialized model uses its saved connection's credentials and address")
	}
	if m.ProviderID == "comfy" {
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

func (c Catalog) GenerationTarget(row Configured) model.GenerationTarget {
	target := model.GenerationTarget{Kind: row.Generation.Kind, Options: row.Generation.Options, Model: model.Model{ProviderID: row.ProviderID, ID: row.ID}}
	source, ok := c.generationConnection(row.Generation.Connection, row.ProviderID, row.Generation.Kind, false)
	if !ok {
		target.Error = "The configured generation connection is unavailable. Check model settings."
		return target
	}
	request := provider.CatalogModelRequest(source.providerConfig(), source.providerMetadata())
	request.ProxyID, request.CodexCredentialPath = source.ProxyID, source.codexCredentialPath
	target.Connection = &request
	return target
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
