package provider

import modelcfg "vista/internal/model"

// CatalogModelRequest converts the prepared provider config into its wire request.
func CatalogModelRequest(item CatalogConfig, metadata CatalogMetadata) modelcfg.Model {
	apiKey := item.APIKey
	if item.AuthToken != "" {
		apiKey = item.AuthToken
	}
	if item.ProviderID == "google-vertex" && item.AccessToken != "" {
		apiKey = item.AccessToken
	}
	openAIProject := ""
	if item.ProviderID == "" || item.ProviderID == "openai" {
		openAIProject = item.Project
	}
	route := item.ResponsesRoute
	if route == modelcfg.ResponsesRouteUnknown {
		route = metadata.ResponsesRoute
	}
	return modelcfg.Model{Custom: item.ProviderID == "custom", Authentication: item.Authentication, ConnectionID: item.ConnectionID, Headers: item.Headers, Provider: item.Provider, ProviderID: item.ProviderID, ID: item.ID, ResponsesRoute: route, WireID: metadata.WireID, BaseURL: item.BaseURL, APIKey: apiKey, AnthropicAuthToken: item.AuthToken != "", OpenAIOrganization: item.Organization, OpenAIProject: openAIProject, ProjectID: item.ProjectID, DeploymentID: item.DeploymentID, ResourceGroup: item.ResourceGroup, GitLabInstanceURL: item.InstanceURL, VertexAPIKey: item.Provider == "vertex-gemini" && item.APIKey != "", GatewayID: item.GatewayID, AzureNative: metadata.AzureNative, AzureDeploymentBasedURLs: item.UseDeploymentURLs != nil && *item.UseDeploymentURLs, APIVersion: item.APIVersion, AppCode: item.AppCode, AWSRegion: item.Region, BedrockAuth: item.BedrockAuth, Tools: item.Tools, Context: item.Context, InputLimit: metadata.InputLimit, OutputLimit: metadata.OutputLimit, ReasoningSummary: item.ReasoningSummary, ReasoningEffort: item.ReasoningEffort, Effort: item.Effort, SafetyIdentifier: item.SafetyIdentifier, PromptCacheKey: item.PromptCacheKey, Thinking: item.Thinking, EnableThinking: item.EnableThinking, ThinkingBudget: item.ThinkingBudget, ThinkingLevel: item.ThinkingLevel, MaxCompletionTokens: item.MaxCompletionTokens, RepetitionPenalty: item.RepetitionPenalty, PreserveThinking: item.PreserveThinking, ClearThinking: item.ClearThinking, ToolStream: item.ToolStream, ParallelToolCalls: item.ParallelToolCalls, ZAIDoSample: item.ZAIDoSample, ChatResponseFormat: item.ChatResponseFormat, ZAIRequestID: item.ZAIRequestID, ZAIUserID: item.ZAIUserID, ReasoningSplit: item.ReasoningSplit, OpenRouter: item.OpenRouter}
}
