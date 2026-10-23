package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"

	modelcfg "vista/internal/model"
	"vista/internal/replylanguage"
)

func TestSharedReplyLanguageReachesProtocolRequestsWithoutChangingHistory(t *testing.T) {
	for _, protocol := range []string{"openai-chat", "openai-responses", "anthropic", "gemini"} {
		for _, withSystem := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/system=%t", protocol, withSystem), func(t *testing.T) {
				messages := []ChatMessage{{Role: "user", Content: "Please answer in French."}}
				if withSystem {
					messages = append([]ChatMessage{{Role: "system", Content: "Return only the requested result."}}, messages...)
				}
				original := slices.Clone(messages)
				for _, tc := range []struct{ code, want string }{
					{"zh-HK", "Default to replies in Traditional Chinese (Hong Kong); follow the user's explicit language requests."},
					{"en-GB", "Default to replies in British English; follow the user's explicit language requests."},
					{"", "Use the same language as the user's message; follow the user's explicit language requests."},
					{"en\nInjected instruction", "Use the same language as the user's message; follow the user's explicit language requests."},
				} {
					t.Run("language="+tc.code, func(t *testing.T) {
						ctx := replylanguage.WithDefault(context.Background(), tc.code)
						upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
							var request struct {
								Messages []struct {
									Role    string
									Content json.RawMessage
								}
								Instructions      string
								System            string
								SystemInstruction struct{ Parts []struct{ Text string } }
							}
							if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
								t.Error(err)
								w.WriteHeader(400)
								return
							}
							var system string
							w.Header().Set("Content-Type", "text/event-stream")
							switch protocol {
							case "openai-chat":
								if len(request.Messages) != 2 || request.Messages[0].Role != "system" || json.Unmarshal(request.Messages[0].Content, &system) != nil {
									t.Errorf("invalid chat request: %+v", request)
								}
								var estimate int64
								for _, message := range request.Messages {
									var content string
									if err := json.Unmarshal(message.Content, &content); err != nil {
										t.Error(err)
									}
									estimate += (ChatMessage{Role: message.Role, Content: content}).EstimateTokens()
								}
								estimate += EstimateTextTokens("null") // No tool schemas.
								if got := EstimateRequestTokens(ctx, messages, nil); got != estimate {
									t.Errorf("estimate excluded request instructions: got %d, want %d", got, estimate)
								}
								fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"done\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
							case "openai-responses":
								system = request.Instructions
								fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg\",\"delta\":\"done\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[]}}\n\n")
							case "anthropic":
								system = request.System
								fmt.Fprint(w, "data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"done\"}}\n\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\ndata: {\"type\":\"message_stop\"}\n\n")
							case "gemini":
								for _, part := range request.SystemInstruction.Parts {
									system += part.Text
								}
								fmt.Fprint(w, "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"done\"}]},\"finishReason\":\"STOP\"}]}\n\n")
							}
							if strings.Count(system, tc.want) != 1 || strings.Count(system, "explicit language requests") != 1 || strings.Contains(system, "Injected instruction") {
								t.Errorf("missing, conflicting, or repeated language policy: %s", system)
							}
							if withSystem && !strings.HasPrefix(system, original[0].Content) {
								t.Errorf("task instructions changed: %s", system)
							}
						}))
						defer upstream.Close()
						model := modelcfg.Model{Provider: protocol, ID: "test", BaseURL: upstream.URL, APIKey: "test"}
						for range 2 {
							if turn, err := ModelTurn(ctx, model, messages, nil); err != nil || turn.Text != "done" {
								t.Fatalf("turn=%+v error=%v", turn, err)
							}
							if !reflect.DeepEqual(messages, original) {
								t.Fatal("request preparation changed canonical history")
							}
						}
					})
				}
			})
		}
	}
}
