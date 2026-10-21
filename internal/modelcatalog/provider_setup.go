package modelcatalog

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"vista/internal/copilotauth"
	"vista/internal/provider"
)

type SetupModel struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// ProviderSetup describes the supported connection forms, not protocol routing.
// Routes, credentials and model capabilities remain owned by provider.
type ProviderSetup struct {
	ID              string       `json:"id"`
	Name            string       `json:"name"`
	Aliases         string       `json:"aliases,omitempty"`
	BaseURL         string       `json:"baseUrl,omitempty"`
	Fields          []string     `json:"fields,omitempty"`
	CredentialModes []string     `json:"credentialModes,omitempty"`
	APIFormats      []string     `json:"apiFormats,omitempty"`
	Models          []SetupModel `json:"models,omitempty"`
}

var providerSetups = []ProviderSetup{
	{ID: "custom", Name: "Custom provider", Aliases: "自定义 OpenAI Anthropic Gemini Cohere", Fields: []string{"baseUrl"}, CredentialModes: []string{"bearer", "x-api-key", "x-goog-api-key", "none"}, APIFormats: provider.CustomProtocols()},
	{ID: "opencode", Name: "OpenCode Zen"},
	{ID: "opencode-go", Name: "OpenCode Zen Go"},
	{ID: "openai", Name: "OpenAI"},
	{ID: "openai-codex", Name: "OpenAI Codex", Aliases: "ChatGPT 账号 订阅 登录", CredentialModes: []string{"oauth"}},
	{ID: "anthropic", Name: "Anthropic", Aliases: "Claude"},
	{ID: "google", Name: "Google Gemini"},
	{ID: "deepseek", Name: "DeepSeek", Aliases: "深度求索"},
	{ID: "xai", Name: "xAI", Aliases: "Grok"},
	{ID: "mistral", Name: "Mistral"},
	{ID: "moonshotai", Name: "Moonshot AI", Aliases: "Kimi 月之暗面 国际"},
	{ID: "moonshotai-cn", Name: "Moonshot AI China", Aliases: "Kimi 月之暗面 中国"},
	{ID: "minimax", Name: "MiniMax", Aliases: "稀宇 国际"},
	{ID: "minimax-cn", Name: "MiniMax China", Aliases: "稀宇 中国"},
	{ID: "zai", Name: "Z.ai", Aliases: "智谱 GLM 国际"},
	{ID: "zai-coding-plan", Name: "Z.ai Coding Plan", Aliases: "智谱 GLM 套餐 国际"},
	{ID: "zhipuai-coding-plan", Name: "Zhipu AI Coding Plan", Aliases: "智谱 GLM 套餐 中国"},
	{ID: "minimax-coding-plan", Name: "MiniMax Coding Plan", Aliases: "稀宇 套餐 国际"},
	{ID: "minimax-cn-coding-plan", Name: "MiniMax China Coding Plan", Aliases: "稀宇 套餐 中国"},
	{ID: "alibaba-coding-plan", Name: "Alibaba Cloud Coding Plan", Aliases: "阿里 百炼 千问 Qwen 套餐 国际"},
	{ID: "kimi-code-plan-global", Name: "Kimi Coding Plan", Aliases: "月之暗面 套餐 国际"},
	{ID: "kimi-code-plan-cn", Name: "Kimi China Coding Plan", Aliases: "月之暗面 套餐 中国"},
	{ID: "openrouter", Name: "OpenRouter"},
	{ID: "groq", Name: "Groq"},
	{ID: "togetherai", Name: "Together AI"},
	{ID: "fireworks-ai", Name: "Fireworks AI"},
	{ID: "cerebras", Name: "Cerebras"},
	{ID: "azure", Name: "Azure OpenAI", Aliases: "Microsoft 微软", Fields: []string{"resourceName", "deploymentId", "apiVersion"}},
	{ID: "amazon-bedrock", Name: "AWS Bedrock", Aliases: "Amazon 亚马逊", Fields: []string{"region"}, CredentialModes: []string{"default", "apiKey"}},
	{ID: "google-vertex", Name: "Google Vertex AI", Aliases: "Gemini Claude Anthropic", Fields: []string{"project", "location"}, CredentialModes: []string{"default", "apiKey", "accessToken"}},
	{ID: "github-copilot", Name: "GitHub Copilot", Aliases: "GitHub 账号 订阅 登录", Fields: []string{"baseUrl"}, CredentialModes: []string{"oauth"}},
	{ID: "ollama", Name: "Ollama", BaseURL: "http://127.0.0.1:11434/v1", Fields: []string{"baseUrl"}, CredentialModes: []string{"default", "apiKey"}},
	{ID: "lmstudio", Name: "LM Studio", BaseURL: "http://127.0.0.1:1234/v1", Fields: []string{"baseUrl"}, CredentialModes: []string{"default", "apiKey"}},
	{ID: "vllm", Name: "vLLM", BaseURL: "http://127.0.0.1:8000/v1", Fields: []string{"baseUrl"}, CredentialModes: []string{"default", "apiKey"}},
}

func setupFor(id string) (ProviderSetup, bool) {
	for _, setup := range providerSetups {
		if setup.ID == id && supportedSetup(id) {
			return setup, true
		}
	}
	return ProviderSetup{}, false
}

func supportedSetup(id string) bool {
	return id == "custom" || LocalProvider(id) || len(provider.CatalogTextModelIDs(id)) > 0
}

func LocalProvider(id string) bool {
	return id == "ollama" || id == "lmstudio" || id == "vllm"
}

func automaticSetup(id string) bool {
	return id == "custom" || ZenProvider(id) || LocalProvider(id) || id == "github-copilot"
}

func catalogInventorySetup(id string) bool {
	_, ok := setupFor(id)
	return ok && !automaticSetup(id) && id != "azure"
}

func ProviderSetups() []ProviderSetup {
	setups := make([]ProviderSetup, 0, len(providerSetups))
	for _, setup := range providerSetups {
		if supportedSetup(setup.ID) {
			setups = append(setups, setup)
		}
	}
	for index := range setups {
		setup := &setups[index]
		if setup.ID == "github-copilot" {
			entry, _ := provider.LookupCatalogProvider(setup.ID)
			setup.BaseURL = entry.BaseURL
		}
		if setup.ID != "azure" {
			continue
		}
		for _, id := range provider.CatalogTextModelIDs(setup.ID) {
			entry, _ := provider.ResolveCatalogModel(setup.ID, id)
			if entry.Package != "@ai-sdk/azure" {
				continue
			}
			setup.Models = append(setup.Models, SetupModel{ID: id, Name: provider.ModelDisplayNameFor(setup.ID, id)})
		}
	}
	return setups
}

// SetupID is computed from raw persisted configuration, before catalog routing.
func SetupID(model Configured) string {
	if model.Provider == "catalog" || LocalProvider(model.ProviderID) || model.ProviderID == "github-copilot" {
		if _, ok := setupFor(model.ProviderID); ok {
			return model.ProviderID
		}
	}
	if model.ProviderID == "custom" && provider.IsCustomProtocol(model.Provider) {
		return "custom"
	}
	return ""
}

func DiscoverConfiguredLocalModels(ctx context.Context, model Configured) ([]Configured, error) {
	return DiscoverLocalModels(ctx, LocalSource{kind: model.ProviderID, baseURL: model.BaseURL, apiKey: model.APIKey})
}

func (input ConnectionInput) configured() Configured {
	model := Configured{Name: input.Name, Provider: input.Provider, ProviderID: input.ProviderID, BaseURL: input.BaseURL, APIKey: input.APIKey,
		Headers: input.Headers, CopilotAuth: input.CopilotAuth,
		AccessToken: input.AccessToken, Region: input.Region, Project: input.Project, Location: input.Location,
		ResourceName: input.ResourceName, DeploymentID: input.DeploymentID, APIVersion: input.APIVersion}
	if input.ProviderID == "custom" {
		model.Authentication = input.CredentialMode
	}
	if input.ProviderID == "amazon-bedrock" {
		model.BedrockAuth = "sigv4"
		if input.CredentialMode == "apiKey" {
			model.BedrockAuth = "bearer"
		}
	}
	return model
}

// prepareSetup validates only fields supported by the chosen form. Empty
// credential inputs reuse a saved secret only for the same destination.
func prepareSetup(input *ConnectionInput, previous *Configured) (ProviderSetup, error) {
	id := "custom"
	if input.Provider == "catalog" || LocalProvider(input.ProviderID) || input.ProviderID == "github-copilot" {
		id = input.ProviderID
	}
	setup, ok := setupFor(id)
	if !ok {
		return setup, errors.New("choose a supported provider")
	}
	if previous != nil && SetupID(*previous) != id {
		return setup, errors.New("disconnect before changing provider type")
	}
	input.BaseURL = strings.TrimSpace(input.BaseURL)
	if !ValidCustomProviderName(input.Name) || len(input.APIKey) > 4096 || len(input.AccessToken) > 4096 {
		return setup, errors.New("provider name or credential exceeds limits")
	}
	fields := map[string]string{"baseUrl": input.BaseURL, "resourceName": input.ResourceName, "deploymentId": input.DeploymentID, "apiVersion": input.APIVersion,
		"region": input.Region, "project": input.Project, "location": input.Location}
	for field, value := range fields {
		if len(value) > 2048 || value != "" && !slices.Contains(setup.Fields, field) {
			return setup, errors.New("connection field is unsupported for this provider: " + field)
		}
	}
	if id == "custom" {
		if !provider.IsCustomProtocol(input.Provider) || input.ProviderID != "custom" || !ValidCustomProviderBaseURL(input.BaseURL) {
			return setup, errors.New("choose a conversation API format and enter an HTTP or HTTPS service address")
		}
		if input.Name == "" {
			u, _ := url.Parse(input.BaseURL)
			input.Name = u.Hostname()
			if len(input.Name) > 64 {
				input.Name = input.Name[:64]
			}
		}
	} else if LocalProvider(id) {
		if input.BaseURL == "" {
			input.BaseURL = setup.BaseURL
		}
		if !ValidProviderBaseURL(input.BaseURL) {
			return setup, errors.New("local model base URL requires HTTPS or loopback HTTP")
		}
		input.Provider = "openai-chat"
	} else {
		input.Provider = "catalog"
		if input.Name == "" {
			input.Name = setup.Name
		}
	}
	if id == "github-copilot" {
		if input.CopilotAuth == nil && previous != nil {
			input.CopilotAuth = previous.CopilotAuth
		}
		if input.CopilotAuth != nil {
			options, err := input.CopilotAuth.Normalize()
			if err != nil {
				return setup, err
			}
			previousOptions := copilotauth.Options{}
			if previous != nil && previous.CopilotAuth != nil {
				previousOptions = *previous.CopilotAuth
			}
			if previous != nil && options != previousOptions && input.APIKey == "" {
				return setup, errors.New("sign in again to change Copilot login settings")
			}
			input.CopilotAuth = &options
		}
		if input.BaseURL == "" {
			entry, _ := provider.LookupCatalogProvider(id)
			input.BaseURL = entry.BaseURL
		}
		input.BaseURL = CopilotRoot(input.BaseURL)
		if !ValidProviderBaseURL(input.BaseURL) {
			return setup, errors.New("Copilot endpoint requires HTTPS or loopback HTTP")
		}
	} else if input.CopilotAuth != nil {
		return setup, errors.New("Copilot login settings are only supported for GitHub Copilot")
	}
	if LocalProvider(id) && input.Name == "" {
		input.Name = setup.Name
	}
	mode := input.CredentialMode
	if mode == "" {
		mode = "apiKey"
		if len(setup.CredentialModes) > 0 {
			mode = setup.CredentialModes[0]
		}
	}
	allowedModes := setup.CredentialModes
	if len(allowedModes) == 0 {
		allowedModes = []string{"apiKey"}
	}
	usesAPIKey := mode == "apiKey" || id == "custom" && mode != "none"
	if !slices.Contains(allowedModes, mode) || !usesAPIKey && !(id == "github-copilot" && mode == "oauth") && input.APIKey != "" || mode != "accessToken" && input.AccessToken != "" {
		return setup, errors.New("credential does not match the selected authentication method")
	}
	input.CredentialMode = mode
	if id != "custom" && len(input.Headers) > 0 {
		return setup, errors.New("custom headers are only supported for custom connections")
	}
	if err := provider.ValidateCustomHeaders(input.Headers); err != nil {
		return setup, err
	}
	previousBase := ""
	if previous != nil {
		previousBase = previous.BaseURL
		if id == "github-copilot" {
			previousBase = CopilotRoot(previousBase)
		}
	}
	sameDestination := previous != nil && (previous.ProviderID == input.ProviderID || id == "custom") && previousBase == input.BaseURL && previous.ResourceName == input.ResourceName && previous.Region == input.Region && previous.Project == input.Project && previous.Location == input.Location
	if sameDestination {
		if (usesAPIKey || id == "github-copilot") && input.APIKey == "" {
			input.APIKey = previous.APIKey
		}
		if mode == "accessToken" && input.AccessToken == "" {
			input.AccessToken = previous.AccessToken
		}
	}
	headers := make(map[string]string, len(input.Headers))
	for name, value := range input.Headers {
		name = http.CanonicalHeaderKey(name)
		if value == "" && sameDestination {
			for savedName, savedValue := range previous.Headers {
				if http.CanonicalHeaderKey(savedName) == name {
					value = savedValue
					break
				}
			}
		}
		if value == "" {
			return setup, errors.New("enter a value for each custom header; re-enter values when changing the address")
		}
		headers[name] = value
	}
	input.Headers = headers
	if id == "github-copilot" && input.APIKey == "" {
		return setup, errors.New("sign in with GitHub to connect Copilot")
	}
	if usesAPIKey && input.APIKey == "" || mode == "accessToken" && input.AccessToken == "" {
		return setup, errors.New("credential is required; re-enter it when changing the endpoint or account")
	}
	return setup, nil
}
