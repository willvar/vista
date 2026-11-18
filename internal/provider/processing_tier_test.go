package provider

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	modelcfg "vista/internal/model"
)

func TestProcessingTiersFollowRouteAndModel(t *testing.T) {
	tests := []struct {
		name              string
		model             modelcfg.Model
		tier, field, want string
	}{
		{"OpenAI Fast", modelcfg.Model{ProviderID: "openai", Provider: "openai-responses", ID: "gpt-6.1-sol"}, "fast", "service_tier", "priority"},
		{"OpenAI Ultrafast", modelcfg.Model{ProviderID: "openai", Provider: "openai-responses", ID: "gpt-6.2-sol"}, "ultrafast", "service_tier", "ultrafast"},
		{"Codex advertised", modelcfg.Model{ProviderID: "openai-codex", Provider: "openai-responses", ID: "gpt-6.2-sol", ResponsesRoute: modelcfg.ResponsesRouteCodex, AdvertisedProcessingTiers: []string{"ultrafast"}}, "ultrafast", "service_tier", "ultrafast"},
		{"Azure Flex deployment", modelcfg.Model{ProviderID: "azure", Provider: "openai-responses", ID: "gpt-6.1-sol", WireID: "deployment", BaseURL: "https://example.openai.azure.com/openai/v1", AzureNative: true}, "flex", "service_tier", "flex"},
		{"Grok", modelcfg.Model{ProviderID: "xai", Provider: "openai-responses", ID: "grok-4.7", BaseURL: "https://api.x.ai/v1"}, "priority", "service_tier", "priority"},
		{"Gemini", modelcfg.Model{ProviderID: "google", Provider: "gemini", ID: "gemini-3.8-flash"}, "priority", "serviceTier", "priority"},
		{"Claude", modelcfg.Model{ProviderID: "anthropic", Provider: "anthropic", ID: "claude-opus-5-5"}, "fast", "speed", "fast"},
		{"Bedrock Mantle", modelcfg.Model{ProviderID: "amazon-bedrock", Provider: "bedrock-mantle-responses", ID: "openai.gpt-6-astra", BaseURL: "https://bedrock-mantle.us-east-1.api.aws/openai/v1", AWSRegion: "us-east-1"}, "ultrafast", "service_tier", "ultrafast"},
		{"Ark low latency", modelcfg.Model{ProviderID: "volcengine", Provider: "openai-chat", ID: "doubao-seed-2-0-pro-260215", BaseURL: "https://ark.cn-beijing.volces.com/api/v3"}, "fast", "service_tier", "fast"},
		{"Ark Flex only", modelcfg.Model{ProviderID: "volcengine", Provider: "openai-chat", ID: "deepseek-v4-1-flash-261017", BaseURL: "https://ark.cn-beijing.volces.com/api/v3"}, "flex", "service_tier", "flex"},
		{"DeepInfra", modelcfg.Model{ProviderID: "deepinfra", Provider: "openai-chat", ID: "deepseek-ai/DeepSeek-V4-Flash-0731", BaseURL: "https://api.deepinfra.com/v1/openai"}, "flex", "service_tier", "flex"},
		{"Groq Standard", modelcfg.Model{ProviderID: "groq", Provider: "openai-chat", ID: "gpt-oss-120b", BaseURL: "https://api.groq.com/openai/v1"}, "standard", "service_tier", "on_demand"},
		{"Mistral Priority", modelcfg.Model{ProviderID: "mistral", Provider: "mistral", ID: "mistral-medium-latest"}, "priority", "service_tier", "auto"},
		{"Fireworks route", modelcfg.Model{ProviderID: "fireworks-ai", Provider: "openai-chat", ID: "accounts/fireworks/models/kimi-k3", BaseURL: "https://api.fireworks.ai/inference/v1"}, "fast", "model", "accounts/fireworks/routers/kimi-k3-fast"},
		{"OpenRouter eligible", modelcfg.Model{ProviderID: "openrouter", Provider: "openai-chat", ID: "openai/gpt-6-astra", BaseURL: "https://openrouter.ai/api/v1", AdvertisedProcessingTiers: []string{"ultrafast"}}, "ultrafast", "service_tier", "ultrafast"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := tc.model
			baseline := map[string]any{"model": m.ID, "input": []string{"stable"}, "prompt_cache_key": "stable-key", "reasoning": map[string]any{"effort": "low"}}
			body := map[string]any{}
			for k, v := range baseline {
				body[k] = v
			}
			applyProcessingBody(m, body)
			if !reflect.DeepEqual(body, baseline) {
				t.Fatal("implicit default changed request")
			}
			if err := ApplyProcessingTier(&m, tc.tier); err != nil {
				t.Fatal(err)
			}
			applyProcessingBody(m, body)
			if body[tc.field] != tc.want {
				t.Fatalf("%s=%v want %q", tc.field, body[tc.field], tc.want)
			}
			if tc.field == "model" {
				baseline["model"] = tc.want
			} else {
				baseline[tc.field] = tc.want
			}
			if !reflect.DeepEqual(body, baseline) {
				t.Fatal("tier changed unrelated prompt/cache settings")
			}
			for _, purpose := range []RequestPurpose{PurposeTitle, PurposeCompaction, PurposeSubagent} {
				if ForPurpose(m, purpose).ProcessingTier != "" {
					t.Fatalf("%s inherited selection", purpose)
				}
			}
		})
	}
	for _, tc := range []struct {
		index int
		tier  string
	}{{1, "performance"}, {8, "flex"}, {9, "fast"}} {
		m := tests[tc.index].model
		if err := ApplyProcessingTier(&m, tc.tier); err == nil {
			t.Fatalf("unlisted tier %s accepted on %s", tc.tier, m.ID)
		}
	}
	for _, tc := range tests {
		m := tc.model
		m.Custom = true
		if err := ApplyProcessingTier(&m, tc.tier); err == nil {
			t.Fatalf("custom route leaked tier for %s", tc.name)
		}
	}
	m := tests[2].model
	m.AdvertisedProcessingTiers = nil
	if err := ApplyProcessingTier(&m, "ultrafast"); err == nil {
		t.Fatal("Codex tier inferred without account advertisement")
	}
	m = tests[3].model
	m.Headers = map[string]string{"x-ms-service-tier": "default"}
	if err := ApplyProcessingTier(&m, "flex"); err == nil {
		t.Fatal("accepted tier conflicting with deployment header override")
	}
	m = tests[10].model
	m.ProcessingTier = "flex"
	if processingHeaderTimeout(m) <= modelHeaderTimeout || processingHeaderTimeout(modelcfg.Model{}) != modelHeaderTimeout {
		t.Fatal("Flex changed global timeout or has no queue budget")
	}
}

func TestProcessingMetadataAndNonBodyTransport(t *testing.T) {
	vertex := modelcfg.Model{ProviderID: "google-vertex", Provider: "vertex-gemini", ID: "gemini-2.5-pro", BaseURL: "https://aiplatform.googleapis.com/v1beta1/projects/example/locations/global/publishers/google"}
	if err := ApplyProcessingTier(&vertex, "flex"); err != nil {
		t.Fatal(err)
	}
	headers := http.Header{}
	applyProcessingHeaders(vertex, headers)
	if headers.Get("X-Vertex-AI-LLM-Request-Type") != "shared" || headers.Get("X-Vertex-AI-LLM-Shared-Request-Type") != "flex" || processingHeaderTimeout(vertex) != 30*time.Minute {
		t.Fatal(headers)
	}
	vercel := modelcfg.Model{ProviderID: "vercel", Provider: "vercel-gateway", ID: "openai/gpt-6.1-sol", BaseURL: "https://ai-gateway.vercel.sh/v3/ai"}
	if err := ApplyProcessingTier(&vercel, "ultrafast"); err != nil {
		t.Fatal(err)
	}
	body := map[string]any{"providerOptions": map[string]any{"anthropic": map[string]any{"cacheControl": true}}}
	applyProcessingBody(vercel, body)
	if got, _ := json.Marshal(body); string(got) != `{"providerOptions":{"anthropic":{"cacheControl":true},"gateway":{"only":["openai"]},"openai":{"serviceTier":"ultrafast"}}}` {
		t.Fatal(string(got))
	}
	if got := parseAdvertisedTiers([]json.RawMessage{[]byte(`{"id":"ultrafast","name":"Ultrafast"}`), []byte(`"priority"`), []byte(`"ultrafast"`), []byte(`"invalid"`)}); !reflect.DeepEqual(got, []string{"ultrafast", "priority"}) {
		t.Fatal(got)
	}
	endpoints := []routingTierEndpoint{{Tag: "openai/ultrafast", Tier: "ultrafast"}, {Tag: "google-vertex/global/flex", Tier: "flex"}}
	if got := routingTiers(endpoints, &modelcfg.OpenRouterRouting{Only: []string{"google-vertex"}}); !reflect.DeepEqual(got, []string{"flex"}) {
		t.Fatal(got)
	}
	codex := modelcfg.Model{ProviderID: "openai-codex", ProcessingTier: "ultrafast"}
	if normalizeProcessingTier(codex, "default") != "" || normalizeProcessingTier(codex, "ultrafast") != "ultrafast" {
		t.Fatal("misinterpreted Codex reply")
	}
	turn, err := parseResponses(strings.NewReader(`data: {"type":"response.completed","response":{"status":"completed","service_tier":"default","output":[{"id":"message-1","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"ok"}]}]}}` + "\n\n"))
	if err != nil || turn.ReportedProcessingTier != "default" || normalizeProcessingTier(modelcfg.Model{ProviderID: "openai"}, turn.ReportedProcessingTier) != "standard" {
		t.Fatalf("response tier: %+v %v", turn, err)
	}
}
