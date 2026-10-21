package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	modelcfg "vista/internal/model"
)

func TestProtocolImageCapabilities(t *testing.T) {
	for _, protocol := range []string{
		"openai-chat", "openai-responses", "anthropic", "vertex-anthropic",
		"gemini", "vertex-gemini", "mistral", "cohere", "bedrock",
		"vercel-gateway", "bedrock-mantle-responses", "watsonx", "sap-ai-core",
	} {
		if !SupportsImageInput(protocol) {
			t.Errorf("%s lost protocol image support", protocol)
		}
	}
	for _, protocol := range []string{"bedrock-mantle-chat", "unknown", ""} {
		if SupportsImageInput(protocol) {
			t.Errorf("%s unexpectedly supports images", protocol)
		}
	}
}

func TestSupportsProtocolIncludesTextOnlyAdapters(t *testing.T) {
	for _, protocol := range []string{"openai-chat", "bedrock-mantle-chat", "vertex-anthropic"} {
		if !SupportsProtocol(protocol) {
			t.Errorf("registered protocol %q was rejected", protocol)
		}
	}
	for _, protocol := range []string{"", "unknown"} {
		if SupportsProtocol(protocol) {
			t.Errorf("unsupported protocol %q was accepted", protocol)
		}
	}
}

func TestModelDispatchValidationPrecedesUnknownProtocol(t *testing.T) {
	for _, test := range []struct {
		name    string
		message ChatMessage
		want    string
	}{
		{"text", ChatMessage{Role: "user", Content: "hello"}, `unsupported model provider "unknown"`},
		{"valid image", ChatMessage{Role: "user", Images: []Image{testImage(t)}}, "image input is not supported by unknown"},
		{"invalid image", ChatMessage{Role: "user", Images: []Image{{MediaType: "image/png"}}}, "image must be 1 byte to 1 MiB"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := ModelTurn(context.Background(), modelcfg.Model{Provider: "unknown"}, []ChatMessage{test.message}, nil)
			if err == nil || err.Error() != test.want {
				t.Fatalf("dispatch error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestModelDispatchPreservesTextAndReasoningCallbacks(t *testing.T) {
	for _, protocol := range []string{"openai-chat", "openai-responses", "bedrock-mantle-chat", "bedrock-mantle-responses"} {
		for _, withReasoning := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/reasoning=%t", protocol, withReasoning), func(t *testing.T) {
				responses := strings.HasSuffix(protocol, "responses")
				endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					path := "/v1/chat/completions"
					if responses {
						path = "/v1/responses"
					}
					if r.Method != http.MethodPost || r.URL.Path != path {
						t.Errorf("request = %s %s, want POST %s", r.Method, r.URL.Path, path)
					}
					w.Header().Set("Content-Type", "text/event-stream")
					if responses {
						if withReasoning {
							fmt.Fprint(w, "data: {\"type\":\"response.reasoning_summary_text.delta\",\"item_id\":\"rs\",\"delta\":\"thinking\"}\n\n")
						}
						fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg\",\"delta\":\"answer\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[]}}\n\n")
						return
					}
					if withReasoning {
						fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"thinking\"}}]}\n\n")
					}
					fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"answer\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
				}))
				defer endpoint.Close()
				model := modelcfg.Model{Provider: protocol, ID: "mock", BaseURL: endpoint.URL + "/v1", APIKey: "test", AWSRegion: "us-east-1"}
				var text, reasoning strings.Builder
				turn, err := ModelTurnStreamWithReasoning(context.Background(), model, []ChatMessage{{Role: "user", Content: "hello"}}, nil,
					func(fragment string) error { text.WriteString(fragment); return nil },
					func(fragment string) error { reasoning.WriteString(fragment); return nil })
				if err != nil || turn.Text != "answer" || text.String() != "answer" {
					t.Fatalf("text dispatch: turn=%+v text=%q err=%v", turn, text.String(), err)
				}
				if withReasoning && reasoning.String() != "thinking" {
					t.Fatalf("reasoning callback = %q", reasoning.String())
				}
			})
		}
	}
}
