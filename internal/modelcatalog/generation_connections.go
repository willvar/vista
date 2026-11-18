package modelcatalog

import (
	"errors"
	"strings"

	"github.com/google/uuid"
	"vista/internal/generation"
	"vista/internal/model"
)

const generationSetupPrefix = "media-"

// A media-only service is a normal saved connection with no conversation model.
func (m Configured) generationOnly() bool {
	return m.Provider == generationProvider && m.Generation == nil
}

func (m Configured) GenerationConnectionRef() string {
	if m.ConnectionID != "" {
		return m.ConnectionID
	}
	return m.Key
}

// Return available wire formats, not a claim about a service or account's
// entitlement. In particular a custom gateway selects its format explicitly
// and keeps its own address and authentication.
func (m Configured) GenerationAdapters(kind string) []string {
	if m.Generation != nil {
		return nil
	}
	id := m.ProviderID
	switch id {
	case "custom":
		switch m.Provider {
		case "openai-chat", "openai-responses":
			switch kind {
			case model.ImageOutput:
				return []string{"openai", "openrouter"}
			case model.VideoOutput:
				return []string{"openrouter", "xai", "deepinfra"}
			case model.DecisionOutput:
				return []string{"openrouter"}
			}
		case "gemini":
			id = "google"
		}
	case "":
		if m.Provider == "openai-chat" || m.Provider == "openai-responses" {
			id = "openai"
		}
	case "togetherai":
		id = "together"
	case "minimax-cn":
		id = "minimax"
	case "alibaba-cn":
		id = "alibaba"
	case "novita-ai":
		id = "novita"
	case "azure":
		id = "microsoft-foundry"
	}
	if _, ok := generation.Find(id, kind); ok {
		return []string{id}
	}
	return nil
}

func generationSetup(spec generation.Spec) ProviderSetup {
	var modes []string
	if spec.ID == "comfy" || spec.ID == "litellm" {
		modes = []string{"none", "apiKey"}
	}
	return ProviderSetup{ID: generationSetupPrefix + spec.ID, Name: spec.Name, BaseURL: spec.Base, Fields: []string{"baseUrl"}, CredentialModes: modes, GenerationOnly: true}
}

func validateGenerationOnlyConnection(m Configured) error {
	if m.ID != "" || m.ProviderID == "openai-codex" || len(m.APIKey) > 4096 || len(m.Headers) != 0 || m.AuthToken != "" || m.AccessToken != "" {
		return errors.New("invalid media service connection")
	}
	if _, ok := setupFor(generationSetupPrefix + m.ProviderID); !ok {
		return errors.New("unknown media service connection")
	}
	_, err := generation.Endpoint(m.BaseURL)
	return err
}

// Old builds attached a credential to each role. Interpret that settings form
// as shared connections without a startup write or changing destination/account.
func (c *Catalog) promoteLegacyGenerationConnections() {
	used := map[string]bool{}
	for _, m := range c.Models {
		used[m.Key] = true
		used[m.ConnectionID] = true
	}
	for i := range c.Models {
		row := c.Models[i]
		if row.Generation == nil || row.Generation.Connection != "" {
			continue
		}
		spec, ok := generation.Find(row.ProviderID, row.Generation.Kind)
		if !ok || spec.ID == "openai-codex" {
			continue
		}
		source := Configured{Provider: generationProvider, ProviderID: row.ProviderID, BaseURL: row.BaseURL, APIKey: row.APIKey, ProxyID: row.ProxyID, Name: spec.Name}
		for _, existing := range c.Models {
			if existing.generationOnly() && !existing.Disabled && existing.ProviderID == source.ProviderID && strings.TrimRight(existing.BaseURL, "/") == strings.TrimRight(source.BaseURL, "/") && existing.APIKey == source.APIKey && existing.ProxyID == source.ProxyID {
				source = existing
				break
			}
		}
		if source.Key == "" {
			// A deterministic identity keeps reads stable until the next settings save.
			source.Key = GeneratedModelKey("media", row.Generation.Kind, used)
			source.ConnectionID = source.Key
			used[source.Key] = true
			c.Models = append(c.Models, source)
		}
		binding := *row.Generation
		binding.Connection = source.GenerationConnectionRef()
		c.Models[i].Generation = &binding
		c.Models[i].BaseURL, c.Models[i].APIKey, c.Models[i].ProxyID = "", "", ""
	}
}

func saveGenerationConnection(raw Catalog, previous *Configured, input ConnectionInput) (Catalog, error) {
	if len(input.IDs) != 0 {
		return raw, errors.New("choose models in Specialized models, not in the service connection")
	}
	row := input.configured()
	if previous != nil {
		row.Key, row.ConnectionID, row.Disabled = previous.Key, previous.ConnectionID, previous.Disabled
		out := append([]Configured(nil), raw.Models...)
		for i, m := range out {
			if m.Key == row.Key {
				out[i] = row
				break
			}
		}
		raw.Models = out
	} else {
		row.ConnectionID = uuid.NewString()
		row.Key = "media-" + row.ConnectionID
		raw.Models = append(append([]Configured(nil), raw.Models...), row)
	}
	return raw, nil
}
