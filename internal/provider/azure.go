package provider

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

	modelcfg "vista/internal/model"
)

func isAzureModel(model modelcfg.Model) bool {
	return model.AzureNative
}

func requireConfiguredAzure(model modelcfg.Model) error {
	if isAzureModel(model) && model.BaseURL == "" {
		return errors.New("Azure model requires a configured resourceName or base URL")
	}
	return nil
}

func openAIEndpoint(base *url.URL, route string, model modelcfg.Model) string {
	endpoint := strings.TrimRight(base.String(), "/")
	if !isAzureModel(model) {
		return endpoint + route
	}
	azureHost := strings.HasSuffix(strings.ToLower(base.Hostname()), ".openai.azure.com")
	if model.AzureDeploymentBasedURLs {
		endpoint += "/deployments/" + url.PathEscape(requestModelID(model))
	} else if azureHost && !strings.HasSuffix(strings.TrimRight(base.Path, "/"), "/v1") {
		endpoint += "/v1"
	}
	endpoint += route
	version := model.APIVersion
	if version == "" && (azureHost || model.AzureDeploymentBasedURLs) {
		version = "v1"
	}
	if version != "" {
		endpoint += "?api-version=" + url.QueryEscape(version)
	}
	return endpoint
}

func setOpenAIAuth(request *http.Request, model modelcfg.Model) {
	if isBedrockMantle(model) && model.APIKey == "" {
		return // The Mantle transport signs with AWS SigV4 after the body is encoded.
	}
	if isAzureModel(model) {
		request.Header.Set("api-key", model.APIKey)
		return
	}
	if model.APIKey == "" {
		return
	}
	request.Header.Set("Authorization", "Bearer "+model.APIKey)
	if (model.Provider == "openai-chat" || model.Provider == "openai-responses") && (model.ProviderID == "" || model.ProviderID == "openai") {
		if model.OpenAIOrganization != "" {
			request.Header.Set("OpenAI-Organization", model.OpenAIOrganization)
		}
		if model.OpenAIProject != "" {
			request.Header.Set("OpenAI-Project", model.OpenAIProject)
		}
	}
}
