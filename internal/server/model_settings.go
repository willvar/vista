package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"vista/internal/copilotauth"
	"vista/internal/modelcatalog"
	"vista/internal/provider"
)

type modelSetting struct {
	Kind           string               `json:"kind,omitempty"`
	Key            string               `json:"key"`
	ConnectionID   string               `json:"connectionId,omitempty"`
	Name           string               `json:"name,omitempty"`
	Provider       string               `json:"provider"`
	ProviderID     string               `json:"providerId,omitempty"`
	ID             string               `json:"id"`
	BaseURL        string               `json:"baseUrl,omitempty"`
	HeaderNames    []string             `json:"headerNames,omitempty"`
	CopilotAuth    *copilotauth.Options `json:"copilotAuth,omitempty"`
	HasAPIKey      bool                 `json:"hasApiKey"`
	Editable       bool                 `json:"editable"`
	HasAccessToken bool                 `json:"hasAccessToken"`
	CredentialMode string               `json:"credentialMode,omitempty"`
	Region         string               `json:"region,omitempty"`
	Project        string               `json:"project,omitempty"`
	Location       string               `json:"location,omitempty"`
	ResourceName   string               `json:"resourceName,omitempty"`
	DeploymentID   string               `json:"deploymentId,omitempty"`
	APIVersion     string               `json:"apiVersion,omitempty"`
}

func (a *App) modelSettings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		w.Header().Set("Cache-Control", "no-store")
		snapshot, err := a.modelSettingsSnapshot()
		catalog, exists := snapshot.raw, snapshot.exists
		if err != nil {
			settingsHTTPError(w, err, 500)
			return
		}
		items := make([]modelSetting, 0, len(catalog.Models))
		for _, model := range catalog.Models {
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
			items = append(items, modelSetting{Kind: modelcatalog.SetupID(model), Key: model.Key, ConnectionID: model.ConnectionID, Name: model.Name, Provider: model.Provider, ProviderID: model.ProviderID, ID: model.ID, BaseURL: model.BaseURL, HasAPIKey: model.APIKey != "", Editable: exists && modelcatalog.Editable(model),
				HeaderNames: headerNames, CopilotAuth: model.CopilotAuth, HasAccessToken: model.AccessToken != "", CredentialMode: mode, Region: model.Region, Project: model.Project, Location: model.Location, ResourceName: model.ResourceName, DeploymentID: model.DeploymentID, APIVersion: model.APIVersion})
		}
		jsonOut(w, map[string]any{"default": catalog.Default, "models": items})
	case http.MethodPatch:
		r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
		var input struct {
			Key string `json:"key"`
		}
		if err := decodeSettingsRequest(r.Body, &input); err != nil {
			settingsHTTPError(w, errors.New("invalid model settings request"), 400)
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
		var saved *configuredModel
		for _, model := range snapshot.raw.Models {
			if modelcatalog.SetupID(model) != "custom" || model.BaseURL != input.BaseURL || input.ConnectionID != "" && model.ConnectionID != input.ConnectionID {
				continue
			}
			if saved != nil && !modelcatalog.SameConnection(*saved, model) {
				settingsHTTPError(w, errors.New("multiple provider connections match; select a connection"), 400)
				return
			}
			copy := model
			saved = &copy
		}
		if saved != nil {
			if source.APIKey == "" && input.CredentialMode != "none" {
				source.APIKey = saved.APIKey
			}
			if input.Headers == nil && input.ConnectionID != "" {
				source.Headers = saved.Headers
			}
		}
	}
	if err := provider.ValidateCustomAuthentication(source.Authentication, source.APIKey); err != nil {
		settingsHTTPError(w, err, 400)
		return
	}
	models, err := modelcatalog.DiscoverCustomModels(r.Context(), source)
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
	key := input.APIKey
	if key == "" {
		snapshot, err := a.modelSettingsSnapshot()
		catalog := snapshot.raw
		if err != nil {
			settingsHTTPError(w, err, 500)
			return
		}
		key, err = modelcatalog.SavedProviderKey(catalog, func(model configuredModel) bool {
			return modelcatalog.ZenProvider(model.ProviderID) && model.ProviderID == input.ProviderID && (input.ConnectionID == "" || model.ConnectionID == input.ConnectionID)
		})
		if err != nil {
			settingsHTTPError(w, err, http.StatusBadRequest)
			return
		}
	}
	if key == "" {
		settingsHTTPError(w, errors.New("enter your OpenCode API key to load available models"), 400)
		return
	}
	ids, err := modelcatalog.FetchZenModelIDs(r.Context(), input.ProviderID, key)
	if err != nil {
		settingsHTTPError(w, err, 502)
		return
	}
	supported := make([]string, 0, len(ids))
	unsupported := make([]string, 0)
	for _, id := range ids {
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
