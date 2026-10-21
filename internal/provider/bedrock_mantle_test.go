package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"

	modelcfg "vista/internal/model"
)

func TestBedrockMantleStreamsReasoning(t *testing.T) {
	for _, protocol := range []string{"bedrock-mantle-chat", "bedrock-mantle-responses"} {
		t.Run(protocol, func(t *testing.T) {
			endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				if protocol == "bedrock-mantle-chat" {
					fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"Check.\",\"content\":\"Answer.\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
				} else {
					fmt.Fprint(w, "data: {\"type\":\"response.reasoning_summary_text.delta\",\"item_id\":\"rs_1\",\"delta\":\"Check.\"}\n\ndata: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg_1\",\"delta\":\"Answer.\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[]}}\n\n")
				}
			}))
			defer endpoint.Close()
			model := modelcfg.Model{Provider: protocol, ID: "mock", BaseURL: endpoint.URL + "/v1", AWSRegion: "us-east-1", APIKey: "test-key"}
			var reasoning, text strings.Builder
			turn, err := ModelTurnStreamWithReasoning(context.Background(), model, []ChatMessage{{Role: "user", Content: "solve"}}, nil,
				func(fragment string) error { _, err := text.WriteString(fragment); return err },
				func(fragment string) error { _, err := reasoning.WriteString(fragment); return err })
			if err != nil || turn.Text != "Answer." || text.String() != "Answer." || reasoning.String() != "Check." {
				t.Fatalf("Mantle reasoning: turn=%+v text=%q reasoning=%q err=%v", turn, text.String(), reasoning.String(), err)
			}
			abort := errors.New("reasoning persistence failed")
			if _, err := ModelTurnStreamWithReasoning(context.Background(), model, nil, nil, nil, func(string) error { return abort }); !errors.Is(err, abort) {
				t.Fatalf("Mantle lost reasoning sink error: %v", err)
			}
		})
	}
}

func TestBedrockMantleResponsesImageRequestAndLargeSignedBody(t *testing.T) {
	image := testImage(t)
	image.Data = append(append([]byte(nil), image.Data...), bytes.Repeat([]byte{0}, maxImageBytes-len(image.Data))...)
	dataURL, err := imageDataURL(image)
	if err != nil {
		t.Fatal(err)
	}
	requests := 0
	text := "describe"
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/openai/v1/responses" || !strings.Contains(r.Header.Get("Authorization"), "bedrock-mantle/aws4_request") || r.ContentLength <= int64(len(image.Data)) {
			t.Errorf("Mantle image route, signature, or length: %s, %q, %d", r.URL, r.Header.Get("Authorization"), r.ContentLength)
		}
		var body struct {
			Input []struct {
				Content []map[string]any `json:"content"`
			} `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		} else if len(body.Input) != 1 || len(body.Input[0].Content) != 3 {
			t.Errorf("Mantle image input: %+v", body.Input)
		} else {
			parts := body.Input[0].Content
			if parts[0]["type"] != "input_text" || parts[0]["text"] != text || parts[1]["type"] != "input_image" || parts[2]["type"] != "input_image" || parts[1]["image_url"] != dataURL || parts[2]["image_url"] != dataURL {
				t.Errorf("Mantle image parts changed")
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg\",\"delta\":\"seen\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[]}}\n\n")
	}))
	defer endpoint.Close()
	model := modelcfg.Model{Provider: "bedrock-mantle-responses", ID: "openai.gpt-5.4", BaseURL: endpoint.URL + "/openai/v1", AWSRegion: "us-east-1", AWSAccessID: "test-access", AWSSecret: "test-secret"}
	message := ChatMessage{Role: "user", Content: "describe", Images: []Image{image, image}}
	turn, err := ModelTurn(context.Background(), model, []ChatMessage{message}, nil)
	if err != nil || turn.Text != "seen" || requests != 1 {
		t.Fatalf("Mantle image turn: %+v, requests=%d, err=%v", turn, requests, err)
	}
	text = strings.Repeat("x", 2<<20)
	message.Content = text
	if turn, err := ModelTurn(context.Background(), model, []ChatMessage{message}, nil); err != nil || turn.Text != "seen" || requests != 2 {
		t.Fatalf("large signed media body: turn=%+v requests=%d err=%v", turn, requests, err)
	}
	message.Images[0].Data = make([]byte, maxImageBytes+1)
	if _, err := ModelTurn(context.Background(), model, []ChatMessage{message}, nil); err == nil || !strings.Contains(err.Error(), "1 MiB") || requests != 2 {
		t.Fatalf("Mantle accepted oversized image request: %v, requests=%d", err, requests)
	}
}

func TestBedrockMantleChatRejectsImageInput(t *testing.T) {
	model := modelcfg.Model{Provider: "bedrock-mantle-chat", ID: "openai.gpt-oss-20b"}
	_, err := ModelTurn(context.Background(), model, []ChatMessage{{Role: "user", Images: []Image{testImage(t)}}}, nil)
	if err == nil || !strings.Contains(err.Error(), "image input is not supported") {
		t.Fatalf("Mantle Chat accepted image input: %v", err)
	}
}

func TestBedrockMantleToolContinuation(t *testing.T) {
	for _, test := range []struct {
		name, provider, modelID, basePath, route string
	}{
		{"responses", "bedrock-mantle-responses", "openai.gpt-5.4", "/openai/v1", "/responses"},
		{"chat", "bedrock-mantle-chat", "openai.gpt-oss-20b", "/v1", "/chat/completions"},
	} {
		for _, auth := range []string{"sigv4", "bearer"} {
			t.Run(test.name+"/"+auth, func(t *testing.T) {
				requests := 0
				endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests++
					if r.Method != http.MethodPost || r.URL.Path != test.basePath+test.route {
						t.Errorf("Mantle route: %s %s", r.Method, r.URL.Path)
					}
					wantAuth := "Bearer test-token"
					if auth == "sigv4" {
						wantAuth = "Credential=test-access/"
						if !strings.Contains(r.Header.Get("Authorization"), "bedrock-mantle/aws4_request") {
							t.Errorf("Mantle SigV4 service: %q", r.Header.Get("Authorization"))
						}
					}
					if !strings.Contains(r.Header.Get("Authorization"), wantAuth) {
						t.Errorf("Mantle authorization: %q", r.Header.Get("Authorization"))
					}
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["model"] != test.modelID || body["stream"] != true {
						t.Errorf("Mantle request: %+v, err=%v", body, err)
					}
					if test.name == "responses" {
						include, _ := body["include"].([]any)
						reasoning, _ := body["reasoning"].(map[string]any)
						if len(include) != 1 || include[0] != "reasoning.encrypted_content" || len(reasoning) != 1 || reasoning["summary"] != "auto" {
							t.Errorf("Mantle Responses defaults: include=%v reasoning=%v", include, reasoning)
						}
					}
					if requests == 2 {
						if test.name == "responses" {
							input, _ := body["input"].([]any)
							if len(input) != 3 || input[1].(map[string]any)["id"] != "fc_1" || input[2].(map[string]any)["type"] != "function_call_output" {
								t.Errorf("Mantle Responses continuation: %+v", input)
							}
						} else {
							messages, _ := body["messages"].([]any)
							if len(messages) != 3 || messages[2].(map[string]any)["role"] != "tool" {
								t.Errorf("Mantle Chat continuation: %+v", messages)
							}
						}
					}
					w.Header().Set("Content-Type", "text/event-stream")
					if test.name == "responses" {
						if requests == 1 {
							fmt.Fprint(w, "data: {\"type\":\"response.output_item.added\",\"item\":{\"id\":\"fc_1\",\"type\":\"function_call\",\"call_id\":\"call_1\",\"name\":\"read_file\",\"arguments\":\"\"}}\n\n")
							fmt.Fprint(w, "data: {\"type\":\"response.output_item.done\",\"item\":{\"item_id\":\"fc_1\",\"type\":\"function_call\",\"call_id\":\"call_1\",\"name\":\"read_file\",\"arguments\":\"{\\\"path\\\":\\\"/tmp/a\\\"}\"}}\n\n")
							fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[{\"item_id\":\"fc_1\",\"type\":\"function_call\",\"call_id\":\"call_1\",\"name\":\"read_file\",\"arguments\":\"{\\\"path\\\":\\\"/tmp/a\\\"}\"}]}}\n\n")
						} else {
							fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg_2\",\"delta\":\"done\"}\n\n")
						}
						if requests != 1 {
							fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[]}}\n\n")
						}
					} else if requests == 1 {
						fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"read_file\",\"arguments\":\"{\\\"path\\\":\\\"/tmp/a\\\"}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n")
					} else {
						fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"done\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
					}
				}))
				defer endpoint.Close()
				model := modelcfg.Model{Provider: test.provider, ID: test.modelID, BaseURL: endpoint.URL + test.basePath, AWSRegion: "us-east-1"}
				if test.name == "responses" {
					model.ProviderID = "amazon-bedrock"
				}
				if auth == "sigv4" {
					model.AWSAccessID, model.AWSSecret = "test-access", "test-secret"
				} else {
					model.APIKey = "test-token"
				}
				messages := []ChatMessage{{Role: "user", Content: "read"}}
				schemas := []map[string]any{{"type": "function", "function": map[string]any{"name": "read_file", "parameters": map[string]any{"type": "object"}}}}
				first, err := ModelTurn(context.Background(), model, messages, schemas)
				if err != nil || len(first.Calls) != 1 || first.Calls[0].ID != "call_1" {
					t.Fatalf("Mantle tool turn: %+v, err=%v", first, err)
				}
				messages = append(messages, ChatMessage{Role: "assistant", ToolCalls: first.Calls, ResponseOutput: first.ResponseOutput}, ChatMessage{Role: "tool", ToolCallID: "call_1", Content: "content"})
				second, err := ModelTurn(context.Background(), model, messages, schemas)
				if err != nil || second.Text != "done" || requests != 2 {
					t.Fatalf("Mantle continuation: %+v, requests=%d, err=%v", second, requests, err)
				}
			})
		}
	}
}

func TestBedrockMantleResponsesReasoningOverrides(t *testing.T) {
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Include   []string          `json:"include"`
			Reasoning map[string]string `json:"reasoning"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if len(body.Include) != 1 || body.Include[0] != "reasoning.encrypted_content" || body.Reasoning["effort"] != "high" || body.Reasoning["summary"] != "concise" {
			t.Errorf("Mantle Responses options: %+v", body)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"type":"response.completed","response":{"status":"completed","output":[]}}`+"\n\n")
	}))
	defer endpoint.Close()
	model := modelcfg.Model{Provider: "bedrock-mantle-responses", ProviderID: "amazon-bedrock", ID: "openai.gpt-5.4", BaseURL: endpoint.URL, AWSRegion: "us-east-1", APIKey: "token", ReasoningEffort: "high", ReasoningSummary: "concise"}
	if _, err := ModelTurn(context.Background(), model, []ChatMessage{{Role: "user", Content: "hello"}}, nil); err != nil {
		t.Fatal(err)
	}
}

func TestBedrockMetadataAfterMessageStop(t *testing.T) {
	events := make(chan types.ConverseStreamOutput, 3)
	events <- &types.ConverseStreamOutputMemberContentBlockDelta{Value: types.ContentBlockDeltaEvent{ContentBlockIndex: aws.Int32(0), Delta: &types.ContentBlockDeltaMemberText{Value: "done"}}}
	events <- &types.ConverseStreamOutputMemberMessageStop{Value: types.MessageStopEvent{StopReason: types.StopReasonEndTurn}}
	events <- &types.ConverseStreamOutputMemberMetadata{Value: types.ConverseStreamMetadataEvent{Usage: &types.TokenUsage{InputTokens: aws.Int32(12), OutputTokens: aws.Int32(3)}}}
	close(events)
	turn, err := parseBedrockEvents(events, func() error { return nil }, nil)
	if err != nil || turn.Text != "done" || turn.FinishReason != "end_turn" || turn.Usage == nil || *turn.Usage != (ModelUsage{InputTokens: 12, OutputTokens: 3}) {
		t.Fatalf("metadata after stop: %+v, err=%v", turn, err)
	}
}
