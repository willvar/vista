package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"

	"vista/internal/copilotauth"
	"vista/internal/generation"
	modelcfg "vista/internal/model"
	"vista/internal/modelcatalog"
	"vista/internal/network"
	"vista/internal/provider"
)

type modelSetting struct {
	ConnectionKey  string                  `json:"connectionKey,omitempty"`
	Disabled       bool                    `json:"disabled,omitempty"`
	PromptCaching  *modelcfg.PromptCaching `json:"promptCaching,omitempty"`
	ResponsesRoute modelcfg.ResponsesRoute `json:"responsesRoute,omitempty"`
	ProxyID        string                  `json:"proxyId,omitempty"`
	Kind           string                  `json:"kind,omitempty"`
	Key            string                  `json:"key"`
	ConnectionID   string                  `json:"connectionId,omitempty"`
	Name           string                  `json:"name,omitempty"`
	Provider       string                  `json:"provider"`
	ProviderID     string                  `json:"providerId,omitempty"`
	ID             string                  `json:"id"`
	BaseURL        string                  `json:"baseUrl,omitempty"`
	HeaderNames    []string                `json:"headerNames,omitempty"`
	CopilotAuth    *copilotauth.Options    `json:"copilotAuth,omitempty"`
	HasAPIKey      bool                    `json:"hasApiKey"`
	Editable       bool                    `json:"editable"`
	HasAccessToken bool                    `json:"hasAccessToken"`
	CredentialMode string                  `json:"credentialMode,omitempty"`
	Region         string                  `json:"region,omitempty"`
	Project        string                  `json:"project,omitempty"`
	Location       string                  `json:"location,omitempty"`
	ResourceName   string                  `json:"resourceName,omitempty"`
	DeploymentID   string                  `json:"deploymentId,omitempty"`
	APIVersion     string                  `json:"apiVersion,omitempty"`
}

func (a *App) writeModelSettings(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	snapshot, err := a.modelSettingsSnapshot()
	catalog, exists := snapshot.raw, snapshot.exists
	if err != nil {
		settingsHTTPError(w, err, 500)
		return
	}
	items := make([]modelSetting, 0, len(catalog.Models))
	for _, model := range catalog.Models {
		if model.Generation != nil {
			continue
		}
		mode := "apiKey"
		if model.ProviderID == "custom" {
			mode = model.Authentication
		}
		headerNames := make([]string, 0, len(model.Headers))
		for name := range model.Headers {
			headerNames = append(headerNames, name)
		}
		sort.Strings(headerNames)
		if model.ProviderID == "openai-codex" || model.ProviderID == "github-copilot" {
			mode = "oauth"
		} else if model.AccessToken != "" {
			mode = "accessToken"
		} else if model.APIKey == "" && (modelcatalog.LocalProvider(model.ProviderID) || model.ProviderID == "amazon-bedrock" || model.ProviderID == "google-vertex") || model.BedrockAuth == "sigv4" {
			mode = "default"
		}
		// Group the cards by the same identity used by connection mutations.
		// No key for environment-only entries: a switch must not persist env credentials.
		connectionKey := ""
		if exists {
			for _, anchor := range catalog.Models {
				if modelcatalog.SameConnection(anchor, model) {
					connectionKey = anchor.Key
					break
				}
			}
		}
		items = append(items, modelSetting{ConnectionKey: connectionKey, Disabled: model.Disabled, PromptCaching: model.PromptCaching, ResponsesRoute: model.ResponsesRoute, ProxyID: model.ProxyID, Kind: modelcatalog.SetupID(model), Key: model.Key, ConnectionID: model.ConnectionID, Name: model.Name, Provider: model.Provider, ProviderID: model.ProviderID, ID: model.ID, BaseURL: model.BaseURL, HasAPIKey: model.APIKey != "", Editable: exists && modelcatalog.Editable(model),
			HeaderNames: headerNames, CopilotAuth: model.CopilotAuth, HasAccessToken: model.AccessToken != "", CredentialMode: mode, Region: model.Region, Project: model.Project, Location: model.Location, ResourceName: model.ResourceName, DeploymentID: model.DeploymentID, APIVersion: model.APIVersion})
	}
	jsonOut(w, map[string]any{"default": catalog.Default, "models": items, "settingsRevision": a.modelSettingsVersion(snapshot.revision), "generation": catalog.GenerationSettings(), "generationAdapters": generation.Specs()})
}

func (a *App) modelSettings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		a.writeModelSettings(w)
	case http.MethodPatch:
		r.Body = http.MaxBytesReader(w, r.Body, 32<<10)
		var input struct {
			Key        string                       `json:"key"`
			Revision   string                       `json:"revision,omitempty"`
			Generation *modelcatalog.GenerationEdit `json:"generation,omitempty"`
		}
		if err := decodeSettingsRequest(r.Body, &input); err != nil {
			settingsHTTPError(w, errors.New("invalid model settings request"), 400)
			return
		}
		if input.Generation != nil && input.Key == "" {
			if err := a.editGeneration(r.Context(), *input.Generation, input.Revision); err != nil {
				settingsHTTPError(w, err, 400)
				return
			}
			a.writeModelSettings(w)
			return
		}
		if input.Key == "" || len(input.Key) > 64 {
			settingsHTTPError(w, errors.New("model name is required"), 400)
			return
		}
		err := a.selectDefaultModel(r.Context(), input.Key)
		if err != nil {
			settingsHTTPError(w, err, 400)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		settingsHTTPError(w, errors.New("method"), 405)
	}
}

func decodeSettingsRequest(body io.Reader, value any) error {
	decoder := json.NewDecoder(body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return errors.New("extra request data")
	}
	return nil
}

func (a *App) discoverCustomModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		settingsHTTPError(w, errors.New("method"), 405)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	var input struct {
		Provider       string            `json:"provider"`
		BaseURL        string            `json:"baseUrl"`
		ConnectionID   string            `json:"connectionId"`
		APIKey         string            `json:"apiKey"`
		CredentialMode string            `json:"credentialMode"`
		Headers        map[string]string `json:"headers"`
	}
	if err := decodeSettingsRequest(r.Body, &input); err != nil || !provider.IsCustomProtocol(input.Provider) || !modelcatalog.ValidCustomProviderBaseURL(input.BaseURL) || len(input.BaseURL) > 2048 || len(input.APIKey) > 4096 {
		settingsHTTPError(w, errors.New("enter a valid custom service connection"), 400)
		return
	}
	source := configuredModel{Provider: input.Provider, ProviderID: "custom", Authentication: input.CredentialMode, BaseURL: input.BaseURL, APIKey: input.APIKey, Headers: input.Headers}
	if source.APIKey == "" && input.CredentialMode != "none" || input.ConnectionID != "" {
		snapshot, err := a.modelSettingsSnapshot()
		if err != nil {
			settingsHTTPError(w, err, 500)
			return
		}
		saved, err := modelcatalog.SavedProviderConnection(snapshot.raw, func(model configuredModel) bool {
			return modelcatalog.SetupID(model) == "custom" && model.BaseURL == input.BaseURL && (input.ConnectionID == "" || model.ConnectionID == input.ConnectionID)
		})
		if err != nil {
			settingsHTTPError(w, err, http.StatusBadRequest)
			return
		}
		if saved != nil {
			source.ProxyID = saved.ProxyID
			if source.APIKey == "" && input.CredentialMode != "none" {
				source.APIKey = saved.APIKey
			}
			if input.Headers == nil && input.ConnectionID != "" {
				source.Headers = saved.Headers
			}
		} else if input.ConnectionID != "" {
			settingsHTTPError(w, errors.New("provider connection is no longer available"), http.StatusNotFound)
			return
		}
	}
	if err := provider.ValidateCustomAuthentication(source.Authentication, source.APIKey); err != nil {
		settingsHTTPError(w, err, 400)
		return
	}
	ctx, err := source.ConnectionContext(network.WithConfigDir(r.Context(), a.configDir))
	if err != nil {
		settingsHTTPError(w, err, http.StatusBadRequest)
		return
	}
	models, err := modelcatalog.DiscoverModels(ctx, source)
	if err != nil {
		settingsHTTPError(w, err, 502)
		return
	}
	ids := make([]string, 0, len(models))
	for _, model := range models {
		ids = append(ids, model.ID)
	}
	w.Header().Set("Cache-Control", "no-store")
	jsonOut(w, map[string]any{"models": ids})
}

func (a *App) discoverZenModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		settingsHTTPError(w, errors.New("method"), 405)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	var input struct {
		ProviderID   string `json:"providerId"`
		ConnectionID string `json:"connectionId"`
		APIKey       string `json:"apiKey"`
	}
	if err := decodeSettingsRequest(r.Body, &input); err != nil || !modelcatalog.ZenProvider(input.ProviderID) || len(input.APIKey) > 4096 {
		settingsHTTPError(w, errors.New("choose OpenCode Zen or Zen Go"), 400)
		return
	}
	source := configuredModel{ProviderID: input.ProviderID, APIKey: input.APIKey}
	if source.APIKey == "" || input.ConnectionID != "" {
		snapshot, err := a.modelSettingsSnapshot()
		if err != nil {
			settingsHTTPError(w, err, 500)
			return
		}
		saved, err := modelcatalog.SavedProviderConnection(snapshot.raw, func(model configuredModel) bool {
			return modelcatalog.ZenProvider(model.ProviderID) && model.ProviderID == input.ProviderID && (input.ConnectionID == "" || model.ConnectionID == input.ConnectionID)
		})
		if err != nil {
			settingsHTTPError(w, err, http.StatusBadRequest)
			return
		}
		if saved != nil {
			source = *saved
			if input.APIKey != "" {
				source.APIKey = input.APIKey
			}
		} else if input.ConnectionID != "" {
			settingsHTTPError(w, errors.New("provider connection is no longer available"), http.StatusNotFound)
			return
		}
	}
	if source.APIKey == "" {
		settingsHTTPError(w, errors.New("enter your OpenCode API key to load available models"), 400)
		return
	}
	ctx, err := source.ConnectionContext(network.WithConfigDir(r.Context(), a.configDir))
	if err != nil {
		settingsHTTPError(w, err, http.StatusBadRequest)
		return
	}
	models, err := modelcatalog.DiscoverModels(ctx, source)
	if err != nil {
		settingsHTTPError(w, err, 502)
		return
	}
	supported := make([]string, 0, len(models))
	unsupported := make([]string, 0)
	for _, model := range models {
		id := model.ID
		if modelcatalog.SelectableZenTextModel(input.ProviderID, id) {
			supported = append(supported, id)
		} else {
			unsupported = append(unsupported, id)
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	jsonOut(w, map[string]any{"models": supported, "unsupported": unsupported})
}

func containsString(items []string, value string) bool {
	for _, item := range items {
		if item == value {
			return true
		}
	}
	return false
}
