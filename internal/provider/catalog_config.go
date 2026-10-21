package provider

import modelcfg "vista/internal/model"

// CatalogConfig contains only provider request and connection configuration.
// Catalog storage and public UI metadata remain owned by modelcatalog.
type CatalogConfig struct {
	Key                 string
	ConnectionID        string
	Name                string
	Provider            string
	ProviderID          string
	ID                  string
	ResponsesRoute      modelcfg.ResponsesRoute
	BaseURL             string
	Route               string
	APIKey              string
	Authentication      string
	Headers             map[string]string
	AuthToken           string
	BedrockAuth         string
	AccessToken         string
	AccountID           string
	GatewayID           string
	ResourceName        string
	APIVersion          string
	UseCompletionURLs   *bool
	UseDeploymentURLs   *bool
	AppCode             string
	Region              string
	WorkspaceID         string
	Project             string
	Organization        string
	ProjectID           string
	DeploymentID        string
	ResourceGroup       string
	Location            string
	InstanceURL         string
	Tools               *bool
	Context             int
	ReasoningSummary    string
	ReasoningEffort     string
	Effort              string
	SafetyIdentifier    string
	PromptCacheKey      string
	Thinking            *modelcfg.ThinkingOption
	EnableThinking      *bool
	ThinkingBudget      *int
	ThinkingLevel       string
	MaxCompletionTokens *int
	RepetitionPenalty   *float64
	PreserveThinking    *bool
	ClearThinking       *bool
	ToolStream          *bool
	ParallelToolCalls   *bool
	ZAIDoSample         *bool
	ChatResponseFormat  *modelcfg.ChatResponseFormat
	ZAIRequestID        string
	ZAIUserID           string
	ReasoningSplit      *bool
	OpenRouter          *modelcfg.OpenRouterOptions
	InputModalities     []string
}

// CatalogMetadata is derived from the selected provider route and model entry.
// Callers keep it separate from the configuration they persist.
type CatalogMetadata struct {
	BaseURLExplicit  bool
	ResponsesRoute   modelcfg.ResponsesRoute
	WireID           string
	InputLimit       int
	OutputLimit      int
	Reasoning        bool
	Resolved         bool
	Status           string
	InputModalities  []string
	OutputModalities []string
	AzureCatalog     bool
	AzureNative      bool
}

// CatalogResolution is the provider-owned result of preparing a catalog model.
type CatalogResolution struct {
	Config                CatalogConfig
	Metadata              CatalogMetadata
	CredentialEnvironment []string
}
