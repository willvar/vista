package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	modelcfg "vista/internal/model"
)

func TestOpenAIProtocolsSendMegabyteTextRequest(t *testing.T) {
	for _, protocol := range []string{"openai-chat", "openai-responses"} {
		t.Run(protocol, func(t *testing.T) {
			text := strings.Repeat("x", 1<<20)
			requests := 0
			endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				var body map[string]json.RawMessage
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				field := "messages"
				if protocol == "openai-responses" {
					field = "input"
				}
				var messages []struct {
					Role    string `json:"role"`
					Content string `json:"content"`
				}
				if err := json.Unmarshal(body[field], &messages); err != nil || len(messages) != 1 || messages[0].Content != text {
					t.Errorf("megabyte message changed: messages=%d err=%v", len(messages), err)
				}
				w.Header().Set("Content-Type", "text/event-stream")
				if protocol == "openai-chat" {
					fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
				} else {
					fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg\",\"delta\":\"ok\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[]}}\n\n")
				}
			}))
			defer endpoint.Close()
			turn, err := ModelTurn(context.Background(), modelcfg.Model{Provider: protocol, ID: "fixture", BaseURL: endpoint.URL, APIKey: "test-key"}, []ChatMessage{{Role: "user", Content: text}}, nil)
			if err != nil || turn.Text != "ok" || requests != 1 {
				t.Fatalf("request=%d turn=%+v err=%v", requests, turn, err)
			}
		})
	}
}

func TestOpenAIProtocolsPreserveJSONSerializationErrors(t *testing.T) {
	for _, protocol := range []string{"openai-chat", "openai-responses"} {
		t.Run(protocol, func(t *testing.T) {
			requests := 0
			endpoint := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests++ }))
			defer endpoint.Close()
			schemas := []map[string]any{{"type": "function", "function": map[string]any{"name": "test", "parameters": map[string]any{"invalid": make(chan int)}}}}
			_, err := ModelTurn(context.Background(), modelcfg.Model{Provider: protocol, ID: "fixture", BaseURL: endpoint.URL, APIKey: "test-key"}, []ChatMessage{{Role: "user", Content: "Hi"}}, schemas)
			var encodingError *json.UnsupportedTypeError
			if !errors.As(err, &encodingError) || requests != 0 {
				t.Fatalf("serialization error: requests=%d err=%v", requests, err)
			}
		})
	}
}

func TestBedrockReplayAllowsLongTextAndStillValidatesReasoning(t *testing.T) {
	text := strings.Repeat("x", 1<<20)
	parts, err := bedrockReplayParts([]BedrockPart{{Kind: "text", Text: text}})
	if err != nil || len(parts) != 1 {
		t.Fatalf("long replay rejected: parts=%d err=%v", len(parts), err)
	}
	_, err = bedrockReplayParts([]BedrockPart{{Kind: "reasoning", Text: strings.Repeat("r", (64<<10)+1), Signature: "signature"}})
	if err == nil {
		t.Fatal("oversized reasoning field accepted")
	}
}
