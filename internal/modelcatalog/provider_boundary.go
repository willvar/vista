package modelcatalog

import (
	modelcfg "vista/internal/model"
	"vista/internal/provider"
)

func (m Configured) providerConfig() provider.CatalogConfig {
	return provider.CatalogConfig{
		Key:                 m.Key,
		ConnectionID:        m.ConnectionID,
		Name:                m.Name,
		Provider:            m.Provider,
		ProviderID:          m.ProviderID,
		ID:                  m.ID,
		ResponsesRoute:      m.ResponsesRoute,
		BaseURL:             m.BaseURL,
		Route:               m.Route,
		APIKey:              m.APIKey,
		Authentication:      m.Authentication,
		Headers:             m.Headers,
		AuthToken:           m.AuthToken,
		BedrockAuth:         m.BedrockAuth,
		AccessToken:         m.AccessToken,
		AccountID:           m.AccountID,
		GatewayID:           m.GatewayID,
		ResourceName:        m.ResourceName,
		APIVersion:          m.APIVersion,
		UseCompletionURLs:   m.UseCompletionURLs,
		UseDeploymentURLs:   m.UseDeploymentURLs,
		AppCode:             m.AppCode,
		Region:              m.Region,
		WorkspaceID:         m.WorkspaceID,
		Project:             m.Project,
		Organization:        m.Organization,
		ProjectID:           m.ProjectID,
		DeploymentID:        m.DeploymentID,
		ResourceGroup:       m.ResourceGroup,
		Location:            m.Location,
		InstanceURL:         m.InstanceURL,
		Tools:               m.Tools,
		Context:             m.Context,
		ReasoningSummary:    m.ReasoningSummary,
		ReasoningEffort:     m.ReasoningEffort,
		Effort:              m.Effort,
		SafetyIdentifier:    m.SafetyIdentifier,
		PromptCacheKey:      m.PromptCacheKey,
		Thinking:            m.Thinking,
		EnableThinking:      m.EnableThinking,
		ThinkingBudget:      m.ThinkingBudget,
		ThinkingLevel:       m.ThinkingLevel,
		MaxCompletionTokens: m.MaxCompletionTokens,
		RepetitionPenalty:   m.RepetitionPenalty,
		PreserveThinking:    m.PreserveThinking,
		ClearThinking:       m.ClearThinking,
		ToolStream:          m.ToolStream,
		ParallelToolCalls:   m.ParallelToolCalls,
		ZAIDoSample:         m.ZAIDoSample,
		ChatResponseFormat:  m.ChatResponseFormat,
		ZAIRequestID:        m.ZAIRequestID,
		ZAIUserID:           m.ZAIUserID,
		ReasoningSplit:      m.ReasoningSplit,
		OpenRouter:          m.OpenRouter,
		InputModalities:     m.InputModalities,
	}
}

func (m Configured) providerMetadata() provider.CatalogMetadata {
	return provider.CatalogMetadata{
		BaseURLExplicit:  m.baseURLExplicit,
		ResponsesRoute:   m.ResponsesRoute,
		WireID:           m.wireID,
		InputLimit:       m.inputLimit,
		OutputLimit:      m.outputLimit,
		Reasoning:        m.catalogReasoning,
		Resolved:         m.catalogResolved,
		Status:           m.status,
		InputModalities:  m.inputModalities,
		OutputModalities: m.outputModalities,
		AzureCatalog:     m.azureCatalog,
		AzureNative:      m.azureNative,
	}
}

func (m *Configured) applyProviderResolution(resolved provider.CatalogResolution) {
	config := resolved.Config
	m.Provider = config.Provider
	m.BaseURL = config.BaseURL
	m.APIKey = config.APIKey
	m.InstanceURL = config.InstanceURL
	m.Project = config.Project
	m.Location = config.Location
	m.Region = config.Region
	m.AppCode = config.AppCode
	m.ProjectID = config.ProjectID
	m.DeploymentID = config.DeploymentID
	m.ResourceGroup = config.ResourceGroup
	m.GatewayID = config.GatewayID
	m.Tools = config.Tools
	m.Context = config.Context
	m.baseURLExplicit = resolved.Metadata.BaseURLExplicit
	if !resolved.Metadata.Resolved {
		return
	}
	if m.ResponsesRoute == modelcfg.ResponsesRouteUnknown {
		m.ResponsesRoute = resolved.Metadata.ResponsesRoute
	}
	m.wireID = resolved.Metadata.WireID
	m.inputLimit = resolved.Metadata.InputLimit
	m.outputLimit = resolved.Metadata.OutputLimit
	m.catalogReasoning = resolved.Metadata.Reasoning
	m.catalogResolved = resolved.Metadata.Resolved
	m.status = resolved.Metadata.Status
	m.inputModalities = resolved.Metadata.InputModalities
	m.outputModalities = resolved.Metadata.OutputModalities
	m.azureCatalog = resolved.Metadata.AzureCatalog
	m.azureNative = resolved.Metadata.AzureNative
}
