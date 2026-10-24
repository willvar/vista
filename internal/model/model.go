// Package model defines model request configuration shared by selection,
// protocol adapters, and the worker transport. It owns no transport or run state.
package model

import (
	"encoding/json"
	"time"
)

// ResponsesRoute is retained only as a transport hint for the Codex
// Responses route. It does not enable or select web search.
type ResponsesRoute string

const (
	ResponsesRouteUnknown ResponsesRoute = ""
	ResponsesRouteCodex   ResponsesRoute = "codex"
)

func (b ResponsesRoute) Valid() bool {
	switch b {
	case ResponsesRouteUnknown, ResponsesRouteCodex:
		return true
	default:
		return false
	}
}

// Model is the resolved configuration for a model request. Provider identifies
// the wire protocol; ProviderID identifies the service using that protocol.
// JSON tags are shared by the worker transport, including optional zero values.
type Model struct {
	// CredentialRef binds a Worker request to its server-owned credential source.
	CredentialRef       string    `json:"credentialRef,omitempty"`
	CredentialExpiresAt time.Time `json:"credentialExpiresAt,omitzero"`

	Provider                 string            `json:"provider"`
	ProviderID               string            `json:"providerId,omitempty"`
	ResponsesRoute           ResponsesRoute    `json:"responsesRoute,omitempty"`
	Key                      string            `json:"key,omitempty"`
	SessionID                string            `json:"sessionId,omitempty"`
	InteractionType          string            `json:"interactionType,omitempty"`
	ID                       string            `json:"id"`
	WireID                   string            `json:"wireId,omitempty"`
	BaseURL                  string            `json:"baseUrl,omitempty"`
	APIKey                   string            `json:"apiKey,omitempty"`
	Authentication           string            `json:"authentication,omitempty"`
	Custom                   bool              `json:"custom,omitempty"`
	ConnectionID             string            `json:"connectionId,omitempty"`
	Headers                  map[string]string `json:"headers,omitempty"`
	CodexCredentialPath      string            `json:"codexCredentialPath,omitempty"`
	AnthropicAuthToken       bool              `json:"anthropicAuthToken,omitempty"`
	OpenAIOrganization       string            `json:"openAIOrganization,omitempty"`
	OpenAIProject            string            `json:"openAIProject,omitempty"`
	ProjectID                string            `json:"projectId,omitempty"`
	DeploymentID             string            `json:"deploymentId,omitempty"`
	ResourceGroup            string            `json:"resourceGroup,omitempty"`
	GitLabHeaders            map[string]string `json:"gitlabHeaders,omitempty"`
	GitLabInstanceURL        string            `json:"gitlabInstanceUrl,omitempty"`
	VertexAPIKey             bool              `json:"vertexApiKey,omitempty"`
	GatewayID                string            `json:"gatewayId,omitempty"`
	AzureNative              bool              `json:"azureNative,omitempty"`
	AzureDeploymentBasedURLs bool              `json:"azureDeploymentBasedUrls,omitempty"`
	APIVersion               string            `json:"apiVersion,omitempty"`
	AppCode                  string            `json:"appCode,omitempty"`
	AWSRegion                string            `json:"awsRegion,omitempty"`
	BedrockAuth              string            `json:"bedrockAuth,omitempty"`
	AWSAccessID              string            `json:"awsAccessId,omitempty"`
	AWSSecret                string            `json:"awsSecret,omitempty"`
	AWSSession               string            `json:"awsSession,omitempty"`
	ImageInput               *bool             `json:"imageInput,omitempty"`
	Tools                    *bool             `json:"tools,omitempty"`
	Context                  int               `json:"context,omitempty"`
	InputLimit               int               `json:"inputLimit,omitempty"`
	OutputLimit              int               `json:"outputLimit,omitempty"`
	ReasoningSummary         string            `json:"reasoningSummary,omitempty"`
	ReasoningEffort          string            `json:"reasoningEffort,omitempty"`
	ReasoningChoice          string            `json:"reasoningChoice,omitempty"`
	// ReasoningDisabled is an internal request-purpose marker. It omits
	// reasoning controls for lightweight requests such as session titles;
	// providers may still use their own default behavior.
	ReasoningDisabled     bool                `json:"-"`
	ChatReasoningOverride bool                `json:"chatReasoningOverride,omitempty"`
	Effort                string              `json:"effort,omitempty"`
	SafetyIdentifier      string              `json:"safetyIdentifier,omitempty"`
	PromptCacheKey        string              `json:"promptCacheKey,omitempty"`
	Thinking              *ThinkingOption     `json:"thinking,omitempty"`
	EnableThinking        *bool               `json:"enableThinking,omitempty"`
	ThinkingBudget        *int                `json:"thinkingBudget,omitempty"`
	ThinkingLevel         string              `json:"thinkingLevel,omitempty"`
	MaxCompletionTokens   *int                `json:"maxCompletionTokens,omitempty"`
	RepetitionPenalty     *float64            `json:"repetitionPenalty,omitempty"`
	PreserveThinking      *bool               `json:"preserveThinking,omitempty"`
	ClearThinking         *bool               `json:"clearThinking,omitempty"`
	ToolStream            *bool               `json:"toolStream,omitempty"`
	ParallelToolCalls     *bool               `json:"parallelToolCalls,omitempty"`
	ZAIDoSample           *bool               `json:"zaiDoSample,omitempty"`
	ChatResponseFormat    *ChatResponseFormat `json:"chatResponseFormat,omitempty"`
	ZAIRequestID          string              `json:"zaiRequestId,omitempty"`
	ZAIUserID             string              `json:"zaiUserId,omitempty"`
	ReasoningSplit        *bool               `json:"reasoningSplit,omitempty"`
	OpenRouter            *OpenRouterOptions  `json:"openRouter,omitempty"`
}
type OpenRouterOptions struct {
	AppName   string               `json:"appName,omitempty"`
	AppURL    string               `json:"appUrl,omitempty"`
	APIKeys   map[string]string    `json:"api_keys,omitempty"`
	Usage     *bool                `json:"usage,omitempty"`
	Models    []string             `json:"models,omitempty"`
	Provider  *OpenRouterRouting   `json:"provider,omitempty"`
	Reasoning *OpenRouterReasoning `json:"reasoning,omitempty"`
	Plugins   []OpenRouterPlugin   `json:"plugins,omitempty"`
	User      string               `json:"user,omitempty"`
}
type OpenRouterPlugin struct {
	ID            string         `json:"id"`
	MaxFiles      *int           `json:"max_files,omitempty"`
	PDF           *OpenRouterPDF `json:"pdf,omitempty"`
	AllowedModels []string       `json:"allowed_models,omitempty"`
}
type OpenRouterPDF struct {
	Engine string `json:"engine,omitempty"`
}
type OpenRouterReasoning struct {
	Enabled   *bool  `json:"enabled,omitempty"`
	Exclude   *bool  `json:"exclude,omitempty"`
	Effort    string `json:"effort,omitempty"`
	MaxTokens *int   `json:"max_tokens,omitempty"`
}
type OpenRouterRouting struct {
	Order             []string         `json:"order,omitempty"`
	Only              []string         `json:"only,omitempty"`
	Ignore            []string         `json:"ignore,omitempty"`
	Quantizations     []string         `json:"quantizations,omitempty"`
	AllowFallbacks    *bool            `json:"allow_fallbacks,omitempty"`
	RequireParameters *bool            `json:"require_parameters,omitempty"`
	DataCollection    string           `json:"data_collection,omitempty"`
	Sort              string           `json:"sort,omitempty"`
	ZDR               *bool            `json:"zdr,omitempty"`
	MaxPrice          *OpenRouterPrice `json:"max_price,omitempty"`
}
type OpenRouterPrice struct {
	Prompt     json.RawMessage `json:"prompt,omitempty"`
	Completion json.RawMessage `json:"completion,omitempty"`
	Image      json.RawMessage `json:"image,omitempty"`
	Audio      json.RawMessage `json:"audio,omitempty"`
	Request    json.RawMessage `json:"request,omitempty"`
}
type ThinkingOption struct {
	Type          string                `json:"type"`
	ClearThinking *bool                 `json:"clear_thinking,omitempty"`
	Keep          string                `json:"keep,omitempty"`
	BudgetTokens  *int                  `json:"budget_tokens,omitempty"`
	BudgetAlias   *int                  `json:"budgetTokens,omitempty"`
	Display       string                `json:"display,omitempty"`
	BlockBinding  *ThinkingBlockBinding `json:"block_binding,omitempty"`
}
type ThinkingBlockBinding struct {
	PrefixMismatchBehavior string `json:"prefix_mismatch_behavior,omitempty"`
}
type ChatResponseFormat struct {
	Type       string          `json:"type"`
	JSONSchema json.RawMessage `json:"json_schema,omitempty"`
}
