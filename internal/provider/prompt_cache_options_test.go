package provider

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	modelcfg "vista/internal/model"
	"vista/internal/network"
)

func TestCacheKeysAndCodexRoutingHaveIndependentCapabilities(t *testing.T) {
	for _, tc := range []struct {
		name         string
		options      *modelcfg.PromptCaching
		route        modelcfg.ResponsesRoute
		key, routing bool
	}{
		{"unknown", nil, "", false, false},
		{"cache only", &modelcfg.PromptCaching{Format: "openai"}, "", true, false},
		{"legacy compatible", nil, modelcfg.ResponsesRouteCodexCompatible, true, true},
		{"routing without key", &modelcfg.PromptCaching{Key: testBool(false)}, modelcfg.ResponsesRouteCodexCompatible, false, true},
		{"routing without hints", &modelcfg.PromptCaching{Format: "none"}, modelcfg.ResponsesRouteCodexCompatible, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if (body["prompt_cache_key"] != nil) != tc.key || (r.Header.Get("session-id") != "") != tc.routing {
					t.Errorf("capabilities key=%v session=%q", body["prompt_cache_key"], r.Header.Get("session-id"))
				}
				want := ""
				if requests == 2 && tc.routing {
					want = "sticky"
				}
				if r.Header.Get("x-codex-turn-state") != want {
					t.Error("routing token lifecycle changed")
				}
				w.Header().Set("x-codex-turn-state", "sticky")
				writeCacheFixtureResponse(t, w, "openai-responses")
			}))
			defer server.Close()
			model := modelcfg.Model{Provider: "openai-responses", Custom: true, Authentication: "none", ID: "opaque", BaseURL: server.URL + "/v1", SessionID: "fixture", ResponsesRoute: tc.route, PromptCaching: tc.options}
			ctx := WithRequestState(context.Background(), new(RequestState))
			for range 2 {
				if _, err := ModelTurn(ctx, model, []ChatMessage{{Role: "user", Content: "fixture"}}, nil); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestPromptCachingManualMarkersAndTailBudget(t *testing.T) {
	for _, count := range []int{3, 4, 5} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			var blocks []json.RawMessage
			for range count {
				blocks = append(blocks, json.RawMessage(`{"type":"text","text":"manual","cache_control":{"type":"ephemeral","ttl":"5m"}}`))
			}
			messages := []ChatMessage{{Role: "system", Content: "stable"}, {Role: "assistant", AnthropicBlocks: blocks}}
			system, converted, err := anthropicMessages(messages)
			if err != nil {
				t.Fatal(err)
			}
			body, err := applyPromptCaching(modelcfg.Model{Provider: "anthropic"}, map[string]any{"system": system, "messages": converted}, messages)
			if count > 4 {
				if err == nil {
					t.Fatal("accepted more than four manual markers")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			hints, _ := countCacheMarkers(body)
			if hints != 4 {
				t.Fatalf("marker budget=%d", hints)
			}
			parts := body["messages"].([]any)[0].(map[string]any)["content"].([]any)
			for _, block := range parts {
				if block.(map[string]any)["cache_control"].(map[string]any)["ttl"] != "5m" {
					t.Fatal("overrode a manual lifetime")
				}
			}
		})
	}
	// A text breakpoint leaves following image/opaque reasoning data intact.
	body := map[string]any{"messages": []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": "stable"}, map[string]any{"type": "image", "source": "opaque"}}}}}
	compiled, err := applyPromptCaching(modelcfg.Model{Provider: "anthropic"}, body, nil)
	if err != nil {
		t.Fatal(err)
	}
	parts := compiled["messages"].([]any)[0].(map[string]any)["content"].([]any)
	if parts[0].(map[string]any)["cache_control"] == nil || parts[1].(map[string]any)["cache_control"] != nil {
		t.Fatal("tail did not prefer text")
	}
}

func TestCacheTTLsAndBedrockModelGatingReachWire(t *testing.T) {
	for _, tc := range []struct {
		name, protocol, id, format, ttl, retention string
		markers                                    int
		field, value                               string
	}{
		{"Messages hour", "anthropic", "claude-sonnet-4-5", "", "1h", "", 2, "cache_control", "1h"},
		{"Bedrock hour", "bedrock", "anthropic.claude-sonnet-4-5-20250929-v1:0", "", "1h", "", 2, "cachePoint", "1h"},
		{"Bedrock legacy fallback", "bedrock", "us.anthropic.claude-3-7-sonnet-20250219-v1:0", "", "1h", "", 2, "cachePoint", ""},
		{"Bedrock unsupported", "bedrock", "anthropic.claude-3-haiku-20240307-v1:0", "", "", "", 0, "cachePoint", ""},
		{"Bedrock Nova implicit", "bedrock", "amazon.nova-lite-v1:0", "", "", "", 0, "cachePoint", ""},
		{"Bedrock opaque profile", "bedrock", "arn:aws:bedrock:us-east-1:123456789012:application-inference-profile/opaque", "bedrock", "1h", "", 2, "cachePoint", "1h"},
		{"OpenAI retention", "openai-chat", "gpt-4.1", "", "", "24h", 0, "prompt_cache_retention", "24h"},
		{"Responses lifetime", "openai-responses", "gpt-5.6", "", "30m", "", 0, "prompt_cache_options", "30m"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var received map[string]any
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
					t.Error(err)
				}
				writeCacheFixtureResponse(t, w, tc.protocol)
			}))
			defer server.Close()
			model := modelcfg.Model{Provider: tc.protocol, ID: tc.id, APIKey: "fixture", BaseURL: server.URL, AWSRegion: "us-east-1", PromptCaching: &modelcfg.PromptCaching{Format: tc.format, TTL: tc.ttl, Retention: tc.retention}}
			if tc.protocol == "bedrock" {
				model.APIKey = ""
				model.AWSAccessID = "fixture"
				model.AWSSecret = "fixture"
			}
			if _, err := ModelTurn(context.Background(), model, []ChatMessage{{Role: "system", Content: "stable"}, {Role: "user", Content: "question"}}, nil); err != nil {
				t.Fatal(err)
			}
			hints, points := countCacheMarkers(received)
			if hints+points != tc.markers {
				t.Fatalf("markers=%d/%d want%d", hints, points, tc.markers)
			}
			encoded, _ := json.Marshal(received)
			if tc.field == "prompt_cache_retention" {
				if received[tc.field] != tc.value {
					t.Fatal("retention lost")
				}
			} else if tc.field == "prompt_cache_options" {
				options, _ := received[tc.field].(map[string]any)
				if options["ttl"] != tc.value || options["prewarm"] != nil {
					t.Fatal("invalid request lifetime")
				}
			} else if tc.value != "" {
				if !strings.Contains(string(encoded), `"ttl":"`+tc.value+`"`) {
					t.Fatalf("lifetime missing: %s", encoded)
				}
			} else if strings.Contains(string(encoded), `"ttl"`) {
				t.Fatal("unsupported Bedrock TTL field")
			}
		})
	}
}

func TestAlibabaCacheLifetimeIsServiceManaged(t *testing.T) {
	for _, model := range []modelcfg.Model{
		{Provider: "openai-chat", ProviderID: "alibaba", ID: "qwen-plus"},
		{Provider: "openai-chat", ProviderID: "openrouter", ID: "qwen/qwen3-coder"},
		{Provider: "vercel-gateway", ID: "alibaba/qwen3-coder"},
		{Provider: "openai-chat", Custom: true, PromptCaching: &modelcfg.PromptCaching{Format: "alibaba"}},
	} {
		options := modelcfg.PromptCaching{}
		if model.PromptCaching != nil {
			options = *model.PromptCaching
		}
		options.TTL = "1h"
		model.PromptCaching = &options
		if err := ValidatePromptCaching(model); err == nil {
			t.Fatal("Alibaba accepted an unsupported Anthropic cache lifetime")
		}
	}
}

func TestGeminiExistingCacheBindingPreservesInstructionsAndNativeParts(t *testing.T) {
	messages := []ChatMessage{{Role: "system", Content: "stable rules"}, {Role: "user", Content: "reference"}, {Role: "assistant", Content: "ack"}, {Role: "user", Content: "question"}}
	contents, system, err := geminiContents(messages)
	if err != nil {
		t.Fatal(err)
	}
	prefix := map[string]any{"systemInstruction": map[string]any{"parts": []any{map[string]any{"text": system}}}, "tools": nil, "contents": contents[:2]}
	encoded, _ := json.Marshal(prefix)
	sum := sha256.Sum256(encoded)
	ref := &modelcfg.CachedContent{Name: "cachedContents/fixture", SHA256: hex.EncodeToString(sum[:]), Messages: 2}
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body["cachedContent"] != ref.Name || body["tools"] != nil || body["systemInstruction"] != nil || len(body["contents"].([]any)) != 1 {
			t.Error("cached prefix was duplicated or current suffix lost")
		}
		writeCacheFixtureResponse(t, w, "gemini")
	}))
	defer server.Close()
	model := modelcfg.Model{Provider: "gemini", ID: "gemini-2.5-flash", APIKey: "fixture", BaseURL: server.URL, PromptCaching: &modelcfg.PromptCaching{CachedContent: ref}}
	before, _ := json.Marshal(messages)
	if _, err := GeminiTurn(context.Background(), model, messages, nil); err != nil {
		t.Fatal(err)
	}
	messages[0].Content = "changed rules"
	if _, err := GeminiTurn(context.Background(), model, messages, nil); err == nil || requests != 1 {
		t.Fatal("changed rules silently reused a stale cached resource")
	}
	messages[0].Content = "stable rules"
	after, _ := json.Marshal(messages)
	if string(before) != string(after) {
		t.Fatal("rewrote canonical Gemini history")
	}
	for _, purpose := range []RequestPurpose{PurposeTitle, PurposeSubagent, PurposeCompaction} {
		if ForPurpose(model, purpose).PromptCaching.CachedContent != nil {
			t.Fatal("side request inherited conversation cache resource")
		}
	}
	if !reflect.DeepEqual(model.PromptCaching.CachedContent, ref) {
		t.Fatal("purpose changed saved options")
	}
}

func TestCodexOwnerUsesNetworkConfigurationRatherThanOnlyProxyID(t *testing.T) {
	state := new(RequestState)
	ctx := WithRequestState(context.Background(), state)
	profile := network.Profile{ID: "same", Server: "first.invalid", Port: 1234, Protocol: "http"}
	model := modelcfg.Model{ID: "fixture", SessionID: "session", ProxyID: "same", ResponsesRoute: modelcfg.ResponsesRouteCodexCompatible, Network: &network.Policy{Profile: &profile}}
	req := func() *http.Request { return httptest.NewRequest("POST", "http://gateway.invalid/v1/responses", nil) }
	first, err := prepareCodexRouting(ctx, model, req())
	if err != nil {
		t.Fatal(err)
	}
	first(&http.Response{StatusCode: 200, Header: http.Header{"X-Codex-Turn-State": {"old"}}})
	profile.Server = "second.invalid"
	request := req()
	if _, err := prepareCodexRouting(ctx, model, request); err != nil {
		t.Fatal(err)
	}
	if request.Header.Get("x-codex-turn-state") != "" {
		t.Fatal("edited proxy reused another route's sticky state")
	}
	first(&http.Response{StatusCode: 200, Header: http.Header{"X-Codex-Turn-State": {"late"}}})
	request = req()
	_, _ = prepareCodexRouting(ctx, model, request)
	if request.Header.Get("x-codex-turn-state") != "" {
		t.Fatal("old route seeded the new owner")
	}
}
