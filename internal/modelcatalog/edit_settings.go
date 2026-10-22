package modelcatalog

import (
	"context"
	"errors"
	"net/http"

	"vista/internal/copilotauth"
)

// ConnectionInput describes the editable connection fields, independent of HTTP.
type ConnectionInput struct {
	Key            string               `json:"key"`
	Name           string               `json:"name"`
	Provider       string               `json:"provider"`
	ProviderID     string               `json:"providerId"`
	BaseURL        string               `json:"baseUrl"`
	APIKey         string               `json:"apiKey"`
	Headers        map[string]string    `json:"headers"`
	CopilotAuth    *copilotauth.Options `json:"copilotAuth"`
	IDs            []string             `json:"ids"`
	CredentialMode string               `json:"credentialMode"`
	AccessToken    string               `json:"accessToken"`
	Region         string               `json:"region"`
	Project        string               `json:"project"`
	Location       string               `json:"location"`
	ResourceName   string               `json:"resourceName"`
	DeploymentID   string               `json:"deploymentId"`
	APIVersion     string               `json:"apiVersion"`
}

// SettingsError gives clients stable recovery actions without matching prose.
type SettingsError struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
}

func (e *SettingsError) Error() string { return e.Message }
func NewSettingsError(code, message string, details map[string]any) *SettingsError {
	return &SettingsError{Code: code, Message: message, Details: details}
}

// SelectDefault also persists an anchor for models discovered after connecting.
func SelectDefault(raw, live Catalog, key string) (Catalog, error) {
	for _, model := range raw.Models {
		if model.Key == key {
			raw.Default = key
			return raw, nil
		}
	}
	for _, model := range live.Models {
		if model.Key != key || model.Info().DiscoverySource != "provider" && model.Info().DiscoverySource != "github-copilot" {
			continue
		}
		for _, anchor := range raw.Models {
			if !SameConnection(anchor, model) {
				continue
			}
			persisted := anchor
			persisted.Key, persisted.ID = key, model.ID
			if LocalProvider(model.ProviderID) || model.ProviderID == "github-copilot" {
				persisted.Provider, persisted.BaseURL = model.Provider, model.BaseURL
				persisted.Tools, persisted.Context = model.Tools, model.Context
			}
			raw.Models = append(raw.Models, persisted)
			raw.Default = key
			return raw, nil
		}
	}
	return Catalog{}, errors.New("model is not configured")
}

func ChangeConnection(ctx context.Context, raw Catalog, exists bool, method string, input ConnectionInput) (Catalog, error) {
	var previous *Configured
	for _, model := range raw.Models {
		if method != http.MethodPost && model.Key == input.Key {
			copy := model
			previous = &copy
			break
		}
	}
	if method != http.MethodPost && (previous == nil || !exists || !Editable(*previous)) {
		return Catalog{}, errors.New("provider connection is not editable")
	}
	if method == http.MethodDelete {
		retained := make([]Configured, 0, len(raw.Models))
		for _, model := range raw.Models {
			if !SameConnection(*previous, model) {
				retained = append(retained, model)
			}
		}
		raw.Models = retained
		if len(raw.Models) == 0 {
			raw.Default = ""
			return raw, nil
		}
		foundDefault := false
		for _, model := range raw.Models {
			foundDefault = foundDefault || model.Key == raw.Default
		}
		if !foundDefault {
			raw.Default = raw.Models[0].Key
		}
		return raw, nil
	}

	setup, err := prepareSetup(&input, previous)
	if err != nil {
		return Catalog{}, err
	}
	return replaceConnection(ctx, raw, previous, setup, input)
}
