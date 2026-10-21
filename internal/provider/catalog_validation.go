package provider

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// ValidateCatalogOptions applies service-specific configuration rules. Catalog
// storage validates its own keys and limits before calling this adapter boundary.
func ValidateCatalogOptions(item CatalogConfig, metadata CatalogMetadata) error {
	if item.Authentication != "" && item.ProviderID != "custom" {
		return errors.New("explicit authentication requires a custom connection")
	}
	if len(item.Headers) > 0 {
		if item.ProviderID != "custom" {
			return errors.New("custom headers require a custom connection")
		}
		if err := ValidateCustomHeaders(item.Headers); err != nil {
			return err
		}
	}
	if item.AuthToken != "" && (item.Provider != "anthropic" || item.ProviderID != "" && item.ProviderID != "anthropic" || item.APIKey != "") {
		return fmt.Errorf("model %q Anthropic authToken requires the direct Messages route and cannot be combined with apiKey", item.Key)
	}
	if item.BedrockAuth != "" {
		if item.ProviderID != "amazon-bedrock" || item.BedrockAuth != "bearer" && item.BedrockAuth != "sigv4" {
			return fmt.Errorf("model %q has invalid Bedrock auth mode", item.Key)
		}
		if item.BedrockAuth == "sigv4" && item.APIKey != "" {
			return fmt.Errorf("model %q SigV4 auth does not accept apiKey", item.Key)
		}
		if item.BedrockAuth == "bearer" && item.APIKey == "" {
			return fmt.Errorf("model %q Bedrock bearer auth requires apiKey or AWS_BEARER_TOKEN_BEDROCK", item.Key)
		}
	}
	if item.ProjectID != "" && item.ProviderID != "watsonx" || item.ProviderID == "watsonx" && !validCatalogPathSegment(item.ProjectID) {
		return errors.New("watsonx catalog model needs a valid projectId or WATSONX_AI_PROJECT_ID")
	}
	if item.DeploymentID != "" && (item.ProviderID != "sap-ai-core" && !metadata.AzureNative || !validCatalogPathSegment(item.DeploymentID)) || item.ResourceGroup != "" && item.ProviderID != "sap-ai-core" || item.ProviderID == "sap-ai-core" && !validCatalogPathSegment(item.ResourceGroup) {
		return errors.New("deploymentId or resourceGroup is invalid for this provider")
	}
	if item.AccountID != "" || item.GatewayID != "" {
		if item.ProviderID != "cloudflare-ai-gateway" && item.ProviderID != "cloudflare-workers-ai" || len(item.AccountID) > 128 || len(item.GatewayID) > 128 || item.ProviderID == "cloudflare-workers-ai" && item.GatewayID != "" {
			return errors.New("Cloudflare accountId or gatewayId is invalid")
		}
		for _, ch := range item.AccountID + item.GatewayID {
			if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '-' || ch == '_') {
				return errors.New("Cloudflare accountId or gatewayId contains invalid characters")
			}
		}
	}
	if item.ResourceName != "" {
		if !metadata.AzureCatalog {
			return errors.New("resourceName is only supported for Azure catalog models")
		}
		if err := validateAzureResourceName(item.ResourceName); err != nil {
			return err
		}
	}
	if item.APIVersion != "" {
		if !metadata.AzureNative || len(item.APIVersion) > 32 {
			return errors.New("apiVersion is only supported for Azure native catalog models")
		}
		for _, ch := range item.APIVersion {
			if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '-') {
				return errors.New("Azure apiVersion contains invalid characters")
			}
		}
	}
	if (item.UseCompletionURLs != nil || item.UseDeploymentURLs != nil) && !metadata.AzureNative {
		return errors.New("Azure native URL options require an Azure native catalog model")
	}
	if item.OpenRouter != nil {
		if err := validateOpenRouterOptions(item.ProviderID, item.Provider, item.OpenRouter); err != nil {
			return fmt.Errorf("model %q: %w", item.Key, err)
		}
	}
	if item.Region != "" {
		if item.ProviderID == "amazon-bedrock" {
			if !validAWSRegion(item.Region) {
				return errors.New("invalid Bedrock region")
			}
		} else if item.ProviderID == "alibaba" || item.ProviderID == "alibaba-cn" {
			if item.WorkspaceID != "" {
				if !validAlibabaHostSegment(item.Region) {
					return errors.New("invalid Alibaba region")
				}
			} else if !metadata.BaseURLExplicit && !slices.Contains([]string{"ap-southeast-1", "cn-beijing", "cn-hongkong", "us-east-1"}, item.Region) {
				return errors.New("Alibaba region needs workspaceID or a known host")
			}
		} else {
			return errors.New("region is unsupported for this catalog Provider")
		}
	}
	if item.WorkspaceID != "" && (!slices.Contains([]string{"alibaba", "alibaba-cn"}, item.ProviderID) || item.Region == "" || !validAlibabaHostSegment(item.WorkspaceID)) {
		return errors.New("workspaceID requires an Alibaba region and a valid workspace ID")
	}
	directOpenAI := (item.Provider == "openai-chat" || item.Provider == "openai-responses") && (item.ProviderID == "" || item.ProviderID == "openai")
	if item.Project != "" && (item.ProviderID != "google-vertex" && !directOpenAI || !validCatalogPathSegment(item.Project)) || item.Organization != "" && (!directOpenAI || !validCatalogPathSegment(item.Organization)) {
		return errors.New("project or organization is invalid for this model")
	}
	if (item.Location != "" || item.AccessToken != "") && (item.ProviderID != "google-vertex" || item.Location != "" && !validCatalogPathSegment(item.Location)) {
		return errors.New("location and accessToken are only supported for Google Vertex catalog models")
	}
	if item.ProviderID == "google-vertex" && item.AccessToken != "" && item.APIKey != "" {
		return errors.New("Google Vertex apiKey and accessToken cannot be combined")
	}
	if item.InstanceURL != "" && (item.ProviderID != "gitlab" || !ValidProviderBaseURL(item.InstanceURL)) {
		return errors.New("instanceUrl is only supported for GitLab catalog models")
	}
	if item.AppCode != "" {
		if item.ProviderID != "aihubmix" || len(item.AppCode) > 64 {
			return errors.New("appCode is only supported for Aihubmix catalog models")
		}
		for _, ch := range item.AppCode {
			if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '_' || ch == '-') {
				return errors.New("Aihubmix appCode contains invalid characters")
			}
		}
	}
	if item.Route != "" && catalogAdapterFor(item.ProviderID).resolveRoute == nil {
		return fmt.Errorf("model %q route is unsupported for this catalog Provider", item.Key)
	}
	moonshot := item.ProviderID == "moonshotai" || item.ProviderID == "moonshotai-cn"
	policy := catalogValidationFor(item)
	if item.Thinking != nil {
		if policy.thinking == nil {
			return unsupportedCatalogThinking(item)
		}
		if err := policy.thinking(item); err != nil {
			return err
		}
	}
	geminiRoute := item.Provider == "gemini" || item.Provider == "vertex-gemini"
	if item.ThinkingLevel != "" && !geminiRoute {
		return fmt.Errorf("model %q has invalid Gemini thinkingLevel", item.Key)
	}
	if geminiRoute {
		if err := validateGeminiThinkingOptions(item.ProviderID, item.ID, item.ThinkingLevel, item.ThinkingBudget); err != nil {
			return fmt.Errorf("model %q: %w", item.Key, err)
		}
	}
	if item.EnableThinking != nil || item.ThinkingBudget != nil && !geminiRoute || item.PreserveThinking != nil || item.ClearThinking != nil {
		if !strings.HasPrefix(item.ProviderID, "alibaba") || item.Provider != "openai-chat" && (item.Provider != "openai-responses" || item.ThinkingBudget != nil || item.PreserveThinking != nil || item.ClearThinking != nil) || item.ThinkingBudget != nil && (*item.ThinkingBudget < -1 || *item.ThinkingBudget > 65536) {
			return fmt.Errorf("model %q has invalid Alibaba Chat thinking controls", item.Key)
		}
	}
	if item.MaxCompletionTokens != nil || item.RepetitionPenalty != nil {
		if !strings.HasPrefix(item.ProviderID, "alibaba") || item.Provider != "openai-chat" {
			return fmt.Errorf("model %q maxCompletionTokens and repetitionPenalty require Alibaba Chat", item.Key)
		}
		if item.MaxCompletionTokens != nil && (*item.MaxCompletionTokens < 1 || metadata.OutputLimit > 0 && *item.MaxCompletionTokens > metadata.OutputLimit) {
			return fmt.Errorf("model %q maxCompletionTokens exceeds the pinned output limit", item.Key)
		}
	}
	if item.Effort != "" {
		if item.Provider != "anthropic" || policy.messagesEffort == nil || !policy.messagesEffort(item) {
			return fmt.Errorf("model %q has invalid Messages effort", item.Key)
		}
	}
	if item.SafetyIdentifier != "" {
		if !moonshot || item.Provider != "openai-responses" || len(item.SafetyIdentifier) > 128 || strings.TrimSpace(item.SafetyIdentifier) != item.SafetyIdentifier {
			return fmt.Errorf("model %q has invalid Moonshot Responses safetyIdentifier", item.Key)
		}
		for _, ch := range item.SafetyIdentifier {
			if ch < 0x20 || ch == 0x7f {
				return fmt.Errorf("model %q has invalid Moonshot Responses safetyIdentifier", item.Key)
			}
		}
	}
	if item.PromptCacheKey != "" {
		if item.ProviderID != "xai" || item.Provider != "openai-chat" || len(item.PromptCacheKey) > 128 || strings.TrimSpace(item.PromptCacheKey) != item.PromptCacheKey {
			return fmt.Errorf("model %q has invalid xAI Chat promptCacheKey", item.Key)
		}
		for _, ch := range item.PromptCacheKey {
			if ch < 0x20 || ch == 0x7f {
				return fmt.Errorf("model %q has invalid xAI Chat promptCacheKey", item.Key)
			}
		}
	}
	if item.ToolStream != nil && (item.Provider != "openai-chat" || !strings.HasPrefix(item.ProviderID, "alibaba") && item.ProviderID != "zai" && item.ProviderID != "zhipuai" && item.ProviderID != "zai-coding-plan" && item.ProviderID != "zhipuai-coding-plan") {
		return fmt.Errorf("model %q toolStream requires a ZAI or Alibaba Chat catalog model", item.Key)
	}
	if item.ParallelToolCalls != nil && (!strings.HasPrefix(item.ProviderID, "alibaba") || item.Provider != "openai-chat") {
		return fmt.Errorf("model %q parallelToolCalls requires an Alibaba Chat catalog model", item.Key)
	}
	if item.ChatResponseFormat != nil {
		format := item.ChatResponseFormat
		zai := slices.Contains([]string{"zai", "zhipuai", "zai-coding-plan", "zhipuai-coding-plan"}, item.ProviderID)
		alibaba := strings.HasPrefix(item.ProviderID, "alibaba")
		if item.Provider != "openai-chat" || !zai && !alibaba || !slices.Contains([]string{"text", "json_object"}, format.Type) && !(alibaba && format.Type == "json_schema") {
			return fmt.Errorf("model %q has invalid Chat responseFormat", item.Key)
		}
		if len(format.JSONSchema) > 0 {
			var schema map[string]json.RawMessage
			if !alibaba || format.Type != "json_schema" || json.Unmarshal(format.JSONSchema, &schema) != nil || schema == nil {
				return fmt.Errorf("model %q has invalid Chat json_schema", item.Key)
			}
		}
	}
	if item.ZAIDoSample != nil || item.ZAIRequestID != "" || item.ZAIUserID != "" {
		if item.Provider != "openai-chat" || item.ProviderID != "zai" && item.ProviderID != "zhipuai" && item.ProviderID != "zai-coding-plan" && item.ProviderID != "zhipuai-coding-plan" {
			return fmt.Errorf("model %q native ZAI Chat options require a ZAI Chat catalog model", item.Key)
		}
		for _, id := range []string{item.ZAIRequestID, item.ZAIUserID} {
			if len(id) > 128 || strings.TrimSpace(id) != id {
				return fmt.Errorf("model %q has invalid ZAI requestID or userID", item.Key)
			}
			for _, ch := range id {
				if ch < 0x20 || ch == 0x7f {
					return fmt.Errorf("model %q has invalid ZAI requestID or userID", item.Key)
				}
			}
		}
	}
	if item.ReasoningSplit != nil && (!strings.HasPrefix(item.ProviderID, "minimax") || item.Provider != "openai-chat") {
		return fmt.Errorf("model %q reasoningSplit requires a MiniMax Chat catalog model", item.Key)
	}
	if item.ReasoningEffort != "" {
		if item.Provider != "openai-chat" && item.Provider != "openai-responses" && item.Provider != "bedrock" && item.Provider != "bedrock-mantle-responses" {
			return fmt.Errorf("model %q reasoningEffort is unsupported for this route", item.Key)
		}
		if policy.reasoningEffort == nil {
			return unsupportedCatalogReasoningEffort(item)
		}
		if err := policy.reasoningEffort(item); err != nil {
			return err
		}
	}
	return nil
}

// ValidateCatalogProtocol checks the resolved wire protocol and credentials.
// Keep it separate from options so catalog identity/modality checks retain their
// established validation order.
func ValidateCatalogProtocol(item CatalogConfig, metadata CatalogMetadata) error {
	if item.ProviderID == "custom" {
		if !IsCustomProtocol(item.Provider) || strings.TrimSpace(item.ID) == "" || !validCustomBaseURL(item.BaseURL) {
			return errors.New("custom connection requires a model ID and an HTTP or HTTPS service address")
		}
		return ValidateCustomAuthentication(item.Authentication, item.APIKey)
	}
	if !SupportsProtocol(item.Provider) {
		return fmt.Errorf("model %q uses unsupported provider %q", item.Key, item.Provider)
	}
	if item.ReasoningSummary != "" {
		openAIResponses := item.Provider == "openai-responses" && slices.Contains([]string{"", "openai", "openai-codex", "azure", "azure-cognitive-services"}, item.ProviderID)
		mantleResponses := item.Provider == "bedrock-mantle-responses" && item.ProviderID == "amazon-bedrock"
		if !openAIResponses && !mantleResponses || !slices.Contains([]string{"auto", "concise", "detailed"}, item.ReasoningSummary) {
			return fmt.Errorf("model %q has an unsupported reasoningSummary option", item.Key)
		}
	}
	local := item.Provider == "openai-chat" && slices.Contains([]string{"ollama", "lmstudio", "vllm"}, item.ProviderID)
	if strings.TrimSpace(item.ID) == "" || item.APIKey == "" && item.AuthToken == "" && !local && !protocolAdapters[item.Provider].ambientCredentials && !catalogAdapterFor(item.ProviderID).ambientCredentials {
		return fmt.Errorf("model %q requires an ID and API key", item.Key)
	}
	return nil
}
