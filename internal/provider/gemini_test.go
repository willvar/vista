package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	modelcfg "vista/internal/model"
)

func TestGeminiVisibleThoughtsStaySeparateFromAnswer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"candidates":[{"content":{"parts":[{"thought":true,"text":"Plan."},{"text":"Answer."}]},"finishReason":"STOP"}]}`+"\n\n")
	}))
	defer server.Close()
	model := modelcfg.Model{Provider: "gemini", ID: "mock", BaseURL: server.URL, APIKey: "key"}
	messages := []ChatMessage{{Role: "user", Content: "solve"}}
	var text, reasoning strings.Builder
	turn, err := ModelTurnStreamWithReasoning(context.Background(), model, messages, nil,
		func(fragment string) error { _, err := text.WriteString(fragment); return err },
		func(fragment string) error { _, err := reasoning.WriteString(fragment); return err })
	if err != nil || turn.Text != "Answer." || text.String() != "Answer." || reasoning.String() != "Plan." {
		t.Fatalf("Gemini thought stream: turn=%+v text=%q reasoning=%q err=%v", turn, text.String(), reasoning.String(), err)
	}
	if _, err := GeminiTurn(context.Background(), model, messages, nil); err == nil || !strings.Contains(err.Error(), "requires a sink") {
		t.Fatalf("Gemini thought was silently dropped: %v", err)
	}
	abort := errors.New("reasoning event failed")
	if _, err := ModelTurnStreamWithReasoning(context.Background(), model, messages, nil, nil, func(string) error { return abort }); !errors.Is(err, abort) {
		t.Fatalf("Gemini reasoning sink error was not propagated: %v", err)
	}
}

func TestGeminiThoughtSummariesDoNotSetEffort(t *testing.T) {
	for _, check := range []struct {
		name, protocol, providerID, id string
		disabled                       bool
		want                           map[string]any
	}{
		{"native default", "gemini", "google", "gemini-2.5-flash", false, map[string]any{"includeThoughts": true}},
		{"vertex default", "vertex-gemini", "google-vertex", "gemini-3-flash-preview", false, map[string]any{"includeThoughts": true}},
		{"custom alias", "gemini", "custom", "gemini-flash-latest", false, map[string]any{"includeThoughts": true}},
		{"models prefix", "gemini", "google", "models/gemini-2.5-flash", false, map[string]any{"includeThoughts": true}},
		{"unknown model", "gemini", "custom", "private-model", false, nil},
		{"nonreasoning model", "gemini", "google", "gemini-2.0-flash", false, nil},
		{"title", "gemini", "google", "gemini-2.5-flash", true, nil},
	} {
		t.Run(check.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					GenerationConfig struct {
						ThinkingConfig map[string]any `json:"thinkingConfig"`
					} `json:"generationConfig"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if !reflect.DeepEqual(body.GenerationConfig.ThinkingConfig, check.want) {
					t.Errorf("thinking config = %v, want %v", body.GenerationConfig.ThinkingConfig, check.want)
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, `data: {"candidates":[{"content":{"parts":[{"text":"done"}]},"finishReason":"STOP"}]}`+"\n\n")
			}))
			defer server.Close()
			model := modelcfg.Model{Provider: check.protocol, ProviderID: check.providerID, ID: check.id, BaseURL: server.URL, APIKey: "key", ReasoningDisabled: check.disabled}
			if _, err := ModelTurn(context.Background(), model, []ChatMessage{{Role: "user", Content: "hello"}}, nil); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestGeminiSendsThinkingBudgetForNativeAndVertexRoutes(t *testing.T) {
	budget := 1024
	for _, route := range []string{"gemini", "vertex-gemini"} {
		t.Run(route, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					GenerationConfig struct {
						ThinkingConfig struct {
							Budget          *int `json:"thinkingBudget"`
							IncludeThoughts bool `json:"includeThoughts"`
						} `json:"thinkingConfig"`
					} `json:"generationConfig"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if body.GenerationConfig.ThinkingConfig.Budget == nil || *body.GenerationConfig.ThinkingConfig.Budget != budget || !body.GenerationConfig.ThinkingConfig.IncludeThoughts {
					t.Errorf("thinking config missing from %s request: %+v", route, body)
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, `data: {"candidates":[{"content":{"parts":[{"text":"done"}]},"finishReason":"STOP"}]}`+"\n\n")
			}))
			defer server.Close()
			model := modelcfg.Model{Provider: route, ID: "gemini-2.5-flash", BaseURL: server.URL, APIKey: "key", ThinkingBudget: &budget}
			if _, err := GeminiTurn(context.Background(), model, []ChatMessage{{Role: "user", Content: "hello"}}, nil); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestGeminiSendsThinkingLevelForGeminiThree(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			GenerationConfig struct {
				ThinkingConfig struct {
					Level           string `json:"thinkingLevel"`
					IncludeThoughts bool   `json:"includeThoughts"`
				} `json:"thinkingConfig"`
			} `json:"generationConfig"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body.GenerationConfig.ThinkingConfig.Level != "LOW" || !body.GenerationConfig.ThinkingConfig.IncludeThoughts {
			t.Errorf("Gemini 3 thinking level missing: %+v", body)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"candidates":[{"content":{"parts":[{"text":"done"}]},"finishReason":"STOP"}]}`+"\n\n")
	}))
	defer server.Close()
	model := modelcfg.Model{Provider: "gemini", ID: "gemini-3-flash-preview", BaseURL: server.URL, APIKey: "key", ThinkingLevel: "low"}
	if _, err := GeminiTurn(context.Background(), model, []ChatMessage{{Role: "user", Content: "hello"}}, nil); err != nil {
		t.Fatal(err)
	}
}

func TestGeminiBoundsCumulativeVisibleThoughts(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for range 3 {
			chunk, _ := json.Marshal(map[string]any{"candidates": []any{map[string]any{"content": map[string]any{"parts": []any{map[string]any{"thought": true, "text": strings.Repeat("r", 90<<10)}}}}}})
			fmt.Fprintf(w, "data: %s\n\n", chunk)
		}
	}))
	defer server.Close()
	model := modelcfg.Model{Provider: "gemini", ID: "mock", BaseURL: server.URL, APIKey: "key"}
	_, err := ModelTurnStreamWithReasoning(context.Background(), model, []ChatMessage{{Role: "user", Content: "solve"}}, nil, nil, func(string) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "reasoning exceeds") {
		t.Fatalf("oversized Gemini thoughts accepted: %v", err)
	}
}

func TestGeminiNativeTextAndFunctionTurn(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/v1beta/models/gemini-test:streamGenerateContent" || r.URL.Query().Get("alt") != "sse" || r.Header.Get("x-goog-api-key") != "secret" {
			t.Errorf("wrong Gemini request: %s %s key=%q", r.Method, r.URL, r.Header.Get("x-goog-api-key"))
		}
		var body struct {
			SystemInstruction struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"systemInstruction"`
			Contents []struct {
				Role  string           `json:"role"`
				Parts []map[string]any `json:"parts"`
			} `json:"contents"`
			Tools []struct {
				FunctionDeclarations []struct {
					Name       string         `json:"name"`
					Parameters map[string]any `json:"parameters"`
				} `json:"functionDeclarations"`
			} `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if len(body.SystemInstruction.Parts) != 1 || body.SystemInstruction.Parts[0].Text != "use tools" || len(body.Tools) != 1 || len(body.Tools[0].FunctionDeclarations) != 1 || body.Tools[0].FunctionDeclarations[0].Name != "lookup" || body.Tools[0].FunctionDeclarations[0].Parameters["type"] != "OBJECT" {
			t.Errorf("Gemini schema or system instruction missing: %+v", body)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if requests == 1 {
			if len(body.Contents) != 1 || body.Contents[0].Role != "user" {
				t.Errorf("initial contents: %+v", body.Contents)
			}
			fmt.Fprint(w, "data: {\"candidates\":[{\"content\":{\"parts\":[{\"thought\":true,\"thoughtSignature\":\"opaque-signature\"},{\"functionCall\":{\"id\":\"google-call-1\",\"name\":\"lookup\",\"args\":{\"query\":\"hello\"}}}]},\"finishReason\":\"STOP\"}]}\n\n")
			return
		}
		if len(body.Contents) != 3 || body.Contents[1].Role != "model" || body.Contents[1].Parts[0]["thoughtSignature"] != "opaque-signature" || body.Contents[1].Parts[0]["functionCall"].(map[string]any)["id"] != "google-call-1" || body.Contents[2].Role != "user" || body.Contents[2].Parts[0]["functionResponse"].(map[string]any)["id"] != "google-call-1" {
			t.Errorf("function roundtrip missing: %+v", body.Contents)
		}
		fmt.Fprint(w, "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"Found it.\"}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":31,\"candidatesTokenCount\":7,\"cachedContentTokenCount\":5,\"thoughtsTokenCount\":3}}\n\n")
	}))
	defer server.Close()
	model := modelcfg.Model{Provider: "gemini", ID: "gemini-test", APIKey: "secret", BaseURL: server.URL + "/v1beta"}
	schemas := []map[string]any{{"type": "function", "function": map[string]any{"name": "lookup", "description": "Find a thing", "parameters": map[string]any{"type": "object", "properties": map[string]any{"query": map[string]any{"type": "string"}}, "required": []string{"query"}}}}}
	messages := []ChatMessage{{Role: "system", Content: "use tools"}, {Role: "user", Content: "hello"}}
	first, err := ModelTurn(context.Background(), model, messages, schemas)
	if err != nil || len(first.Calls) != 1 || first.Calls[0].Function.Name != "lookup" || first.Calls[0].ThoughtSignature != "opaque-signature" || first.Calls[0].GeminiID != "google-call-1" {
		t.Fatalf("function turn: %+v %v", first, err)
	}
	messages = append(messages, ChatMessage{Role: "assistant", ToolCalls: first.Calls}, ChatMessage{Role: "tool", ToolCallID: first.Calls[0].ID, Content: `{"result":"hello"}`})
	second, err := ModelTurn(context.Background(), model, messages, schemas)
	if err != nil || second.Text != "Found it." || second.FinishReason != "STOP" || second.Usage == nil || *second.Usage != (ModelUsage{InputTokens: 31, OutputTokens: 10, CacheReadTokens: 5, ReasoningTokens: 3}) || requests != 2 {
		t.Fatalf("text turn: %+v %v, requests=%d", second, err, requests)
	}
}

func TestGeminiRejectsIncompleteAndOversizedFunctionCalls(t *testing.T) {
	for _, test := range []struct {
		name, event, want string
	}{
		{"bad args", `{"candidates":[{"content":{"parts":[{"functionCall":{"name":"lookup","args":"not-json"}}]},"finishReason":"STOP"}]}`, "arguments"},
		{"blocked", `{"candidates":[{"finishReason":"SAFETY"}]}`, "SAFETY"},
		{"bad usage", `{"candidates":[{"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":2,"cachedContentTokenCount":3}}`, "usage"},
		{"unfinished", `{"candidates":[{"content":{"parts":[{"text":"partial"}]}}]}`, "complete"},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprintf(w, "data: %s\n\n", test.event)
			}))
			defer server.Close()
			_, err := GeminiTurn(context.Background(), modelcfg.Model{ID: "gemini-test", APIKey: "secret", BaseURL: server.URL}, []ChatMessage{{Role: "user", Content: "hello"}}, nil)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("unexpected result: %v", err)
			}
		})
	}
}
