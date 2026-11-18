package provider

import (
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	modelcfg "vista/internal/model"
)

// ProcessingOptions describes the actual route, not a model family or the
// OpenAI-compatible API format. Never infer a premium tier from a name suffix.
func ProcessingOptions(m modelcfg.Model) []string {
	if m.Custom || m.ProviderID == "custom" {
		return nil
	}
	u, err := url.Parse(m.BaseURL)
	if err != nil || m.BaseURL != "" && (u.Scheme != "https" || u.User != nil || u.RawQuery != "" || u.Fragment != "") {
		return nil
	}
	host, id := strings.ToLower(u.Host), requestModelID(m)
	is := func(ids ...string) bool { return slices.Contains(ids, id) }
	chat, responses := m.Provider == "openai-chat", m.Provider == "openai-responses"
	var options []string
	add := func(ok bool, names ...string) {
		if ok {
			options = append(options, names...)
		}
	}
	advertises := func(tier string) bool { return slices.Contains(m.AdvertisedProcessingTiers, tier) }
	switch m.ProviderID {
	case "", "openai":
		if (!chat && !responses) || (host != "api.openai.com" && host != "") || m.ResponsesRoute != modelcfg.ResponsesRouteUnknown {
			break
		}
		add(is("gpt-6.2-astra", "gpt-6.2-sol", "gpt-6-astra", "gpt-6-sol", "gpt-6-luna", "gpt-6.1-sol", "gpt-6.1-terra", "gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna", "gpt-5.5", "gpt-5.4", "gpt-5.4-mini", "gpt-5.2", "gpt-5.1", "gpt-5", "gpt-5-mini", "gpt-4.1"), "fast")
		add(is("gpt-6-astra", "gpt-6.1-sol", "gpt-6.1-terra", "gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna", "gpt-5.5", "gpt-5.4", "gpt-5.4-mini", "gpt-5.2", "gpt-5.1", "gpt-5", "gpt-5-mini", "o3", "o4-mini"), "flex")
		add(responses && is("gpt-6.2-astra", "gpt-6.2-sol", "gpt-6-astra", "gpt-6.1-sol"), "ultrafast")
	case "openai-codex":
		if !responses || m.ResponsesRoute != modelcfg.ResponsesRouteCodex || (host != "chatgpt.com" && host != "") {
			break
		}
		add(advertises("priority") || advertises("fast"), "fast")
		add(advertises("flex"), "flex")
		add(advertises("ultrafast"), "ultrafast")
	case "azure", "azure-cognitive-services":
		if !m.AzureNative || (!chat && !responses) || !strings.HasSuffix(host, ".openai.azure.com") {
			break
		}
		id = m.ID // The deployment name on the wire is not the model version.
		add(is("gpt-6-sol", "gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.5", "gpt-5.4", "gpt-5.4-mini", "gpt-5.2", "gpt-5.1", "gpt-4.1"), "priority")
		add(is("gpt-6.1-sol", "gpt-6-sol", "gpt-6-luna", "gpt-6-astra", "gpt-5.6-sol", "gpt-5.6-luna", "gpt-5.6-terra"), "flex")
	case "xai":
		add((chat || responses) && host == "api.x.ai" && is("grok-4.5", "grok-4.6", "grok-4.7"), "priority")
	case "volcengine":
		if (!chat && !responses) || host != "ark.cn-beijing.volces.com" || strings.TrimRight(u.Path, "/") != "/api/v3" {
			break
		}
		add(is("doubao-seed-2-1-lite-260915", "doubao-seed-2-1-turbo-260628", "doubao-seed-2-0-pro-260215", "doubao-seed-2-0-lite-260428", "doubao-seed-2-0-lite-260215", "doubao-seed-2-0-mini-260428"), "fast")
		add(is("deepseek-v4-1-flash-261017", "deepseek-v4-pro-ga-260813", "deepseek-v4-flash-ga-260731", "doubao-seed-2-1-lite-260915", "doubao-seed-2-1-pro-260628", "doubao-seed-2-1-turbo-260628", "doubao-seed-2-0-lite-260428"), "flex")
	case "google", "google-vertex":
		if m.ProviderID == "google" && (m.Provider != "gemini" || host != "" && host != "generativelanguage.googleapis.com") {
			break
		}
		if m.ProviderID == "google-vertex" && (m.Provider != "vertex-gemini" || host != "aiplatform.googleapis.com" || !strings.Contains(u.Path, "/locations/global/")) {
			break
		}
		common := is("gemini-3.8-flash", "gemini-3.6-flash", "gemini-3.5-flash-lite", "gemini-3.1-pro-preview", "gemini-3.1-flash-lite", "gemini-3.1-flash-lite-preview", "gemini-3-flash-preview", "gemini-2.5-pro", "gemini-2.5-flash", "gemini-2.5-flash-lite")
		add(common || m.ProviderID == "google" && is("gemini-4-carbon", "gemini-4-argon", "gemini-4-hydrogen"), "priority", "flex")
	case "anthropic":
		add(m.Provider == "anthropic" && !m.AnthropicAuthToken && (host == "api.anthropic.com" || host == "") && is("claude-opus-5-5", "claude-fable-5-2", "claude-mythos-5-2"), "fast")
	case "amazon-bedrock":
		switch m.Provider {
		case "bedrock":
			add((host == "" || strings.HasPrefix(host, "bedrock-runtime.") && strings.HasSuffix(host, ".amazonaws.com")) && is("us.moonshotai.kimi-k3", "global.moonshotai.kimi-k3", "moonshotai.kimi-k3", "amazon.nova-2-lite-v1:0", "us.amazon.nova-2-lite-v1:0"), "priority", "flex")
		case "bedrock-mantle-chat", "bedrock-mantle-responses":
			if !strings.HasPrefix(host, "bedrock-mantle.") || !strings.HasSuffix(host, ".api.aws") {
				break
			}
			add(is("xai.grok-4.3", "xai.grok-4.6", "xai.grok-4.7"), "priority", "flex")
			add(m.Provider == "bedrock-mantle-responses" && m.AWSRegion == "us-east-1" && is("openai.gpt-6-astra"), "ultrafast")
		}
	case "deepinfra":
		add(chat && host == "api.deepinfra.com" && (is("deepseek-ai/DeepSeek-V4-Flash-0731") || advertises("priority")), "priority")
		add(chat && host == "api.deepinfra.com" && (is("deepseek-ai/DeepSeek-V4-Flash-0731") || advertises("flex")), "flex")
	case "groq":
		add(chat && host == "api.groq.com", "flex")
		add(chat && host == "api.groq.com" && advertises("performance"), "performance")
	case "mistral":
		add(m.Provider == "mistral" && (host == "" || host == "api.mistral.ai") && (is("mistral-medium-latest") || advertises("priority")), "priority")
	case "cerebras":
		// Shared Developer endpoints do not support selection. An unverified
		// dedicated URL is not evidence of an enabled tier or its entitlement.
		add(chat && host == "api.cerebras.ai" && advertises("priority"), "priority")
		add(chat && host == "api.cerebras.ai" && advertises("flex"), "flex")
	case "fireworks-ai":
		if !chat || (host != "api.fireworks.ai" && host != "us.api.fireworks.ai") {
			break
		}
		add(is("accounts/fireworks/models/glm-5p3") || advertises("priority"), "priority")
		for _, tier := range []string{"fast", "ultrafast"} {
			add(fireworksTierModel(id, tier) != "" || m.ProcessingTierModels[tier] != "", tier)
		}
	case "openrouter":
		if !chat || host != "openrouter.ai" {
			break
		}
		for _, tier := range []string{"priority", "flex", "ultrafast"} {
			add(advertises(tier), tier)
		}
	case "vercel":
		if m.Provider != "vercel-gateway" || host != "ai-gateway.vercel.sh" {
			break
		}
		add(is("openai/gpt-6-astra", "openai/gpt-6.1-sol"), "priority", "flex", "ultrafast")
		add(is("openai/gpt-5.6-sol", "google/gemini-3.5-flash-lite"), "priority", "flex")
		add(is("spacexai/grok-4.5", "spacexai/grok-4.6", "spacexai/grok-4.7", "xai/grok-4.5", "xai/grok-4.6", "xai/grok-4.7"), "priority")
	}
	if len(options) == 0 {
		return nil
	}
	// Managed routing headers take precedence over a JSON request body. Don't
	// offer a selector if connection headers would silently defeat it.
	for name := range m.Headers {
		switch strings.ToLower(name) {
		case "openai-service-tier", "x-ms-service-tier", "x-vertex-ai-llm-request-type", "x-vertex-ai-llm-shared-request-type", "x-deepinfra-service-tier", "x-amzn-bedrock-service-tier":
			return nil
		}
	}
	if !slices.Contains([]string{"vercel", "fireworks-ai"}, m.ProviderID) {
		options = append([]string{"standard"}, options...)
	}
	return options
}

func ApplyProcessingTier(m *modelcfg.Model, tier string) error {
	if tier != "" && !slices.Contains(ProcessingOptions(*m), tier) {
		return errors.New("processing tier is unavailable for the selected model and connection; choose another tier or follow the provider default")
	}
	m.ProcessingTier = tier
	return nil
}

func fireworksTierModel(id, tier string) string {
	switch id + "/" + tier {
	case "accounts/fireworks/models/glm-5p3/fast":
		return "accounts/fireworks/routers/glm-5p3-fast"
	case "accounts/fireworks/models/kimi-k3/fast":
		return "accounts/fireworks/routers/kimi-k3-fast"
	case "accounts/fireworks/models/deepseek-v4p1-flash-1017/ultrafast":
		return "accounts/fireworks/routers/deepseek-v4p1-flash-1017-ultrafast"
	}
	return ""
}

func processingTierValue(m modelcfg.Model) string {
	tier := m.ProcessingTier
	if tier == "standard" {
		switch m.ProviderID {
		case "google", "anthropic":
			return "standard"
		case "groq":
			return "on_demand"
		case "mistral":
			return "standard_only"
		default:
			return "default"
		}
	}
	if tier == "fast" && (m.ProviderID == "openai-codex" || m.ProviderID == "openai" || m.ProviderID == "") {
		return "priority"
	}
	if tier == "priority" && m.ProviderID == "mistral" {
		return "auto"
	}
	return tier
}

// applyProcessingBody updates only provider-owned wire fields. It runs after
// prompt-cache projection, including for bodies copied by the cache adapter.
func applyProcessingBody(m modelcfg.Model, body map[string]any) {
	tier := processingTierValue(m)
	if tier == "" {
		return
	}
	switch m.Provider {
	case "vertex-gemini", "bedrock":
		return
	case "anthropic":
		body["speed"] = tier
	case "gemini":
		body["serviceTier"] = tier
	case "vercel-gateway":
		options, _ := body["providerOptions"].(map[string]any)
		if options == nil {
			options = map[string]any{}
			body["providerOptions"] = options
		}
		if tier == "ultrafast" {
			options["openai"] = map[string]any{"serviceTier": tier}
			options["gateway"] = map[string]any{"only": []string{"openai"}}
		} else {
			options["gateway"] = map[string]any{"serviceTier": tier}
		}
	default:
		if m.ProviderID == "fireworks-ai" {
			if target := m.ProcessingTierModels[m.ProcessingTier]; target != "" {
				body["model"] = target
				return
			}
			if target := fireworksTierModel(requestModelID(m), m.ProcessingTier); target != "" {
				body["model"] = target
				return
			}
		}
		body["service_tier"] = tier
	}
}

func applyProcessingHeaders(m modelcfg.Model, h http.Header) {
	if m.Provider == "vertex-gemini" && m.ProcessingTier != "" {
		h.Set("X-Vertex-AI-LLM-Request-Type", "shared")
		if m.ProcessingTier != "standard" {
			h.Set("X-Vertex-AI-LLM-Shared-Request-Type", m.ProcessingTier)
		}
	}
}

// None of the providers promises that selecting a tier guarantees capacity.
// Missing/ambiguous echoes, in particular on Codex, remain unknown.
func normalizeProcessingTier(m modelcfg.Model, reported string) string {
	switch reported {
	case "default", "standard", "on_demand", "standard_only", "ON_DEMAND":
		if m.ProviderID == "openai-codex" && m.ProcessingTier != "" && m.ProcessingTier != "standard" {
			return ""
		}
		return "standard"
	case "priority", "ON_DEMAND_PRIORITY":
		if m.ProviderID == "openai" || m.ProviderID == "openai-codex" || m.ProviderID == "" && !m.Custom {
			return "fast"
		}
		return "priority"
	case "flex", "ON_DEMAND_FLEX":
		return "flex"
	case "fast", "ultrafast", "performance", "reserved", "scale":
		return reported
	case "PROVISIONED_THROUGHPUT":
		return "reserved"
	default:
		return ""
	}
}

func processingHeaderTimeout(m modelcfg.Model) time.Duration {
	if m.ProcessingTier != "flex" {
		return modelHeaderTimeout
	}
	switch m.ProviderID {
	case "google", "google-vertex":
		return 30 * time.Minute
	case "deepinfra", "openai", "azure", "azure-cognitive-services":
		return 15 * time.Minute
	default:
		return modelHeaderTimeout
	}
}
