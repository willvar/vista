package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"github.com/aws/smithy-go/eventstream"

	modelcfg "vista/internal/model"
)

func TestBedrockConverseImageRequestShapeAndLimits(t *testing.T) {
	image := testImage(t)
	image.Data = append(append([]byte(nil), image.Data...), bytes.Repeat([]byte{0}, maxImageBytes-len(image.Data))...)
	wantBytes := base64.StdEncoding.EncodeToString(image.Data)
	requests := 0
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.ContentLength <= int64(len(image.Data)) || !strings.Contains(r.URL.Path, "/converse-stream") {
			t.Errorf("Bedrock image request length or route: %d, %s", r.ContentLength, r.URL)
		}
		var body struct {
			Messages []struct {
				Role    string           `json:"role"`
				Content []map[string]any `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		} else if len(body.Messages) != 1 || body.Messages[0].Role != "user" || len(body.Messages[0].Content) != 3 {
			t.Errorf("Bedrock image message: %+v", body.Messages)
		} else {
			parts := body.Messages[0].Content
			if parts[0]["text"] != "describe" {
				t.Errorf("Bedrock text part: %+v", parts[0])
			}
			for _, part := range parts[1:] {
				block, ok := part["image"].(map[string]any)
				if !ok || block["format"] != "png" {
					t.Errorf("Bedrock image block: %+v", part)
					continue
				}
				source, ok := block["source"].(map[string]any)
				if !ok || source["bytes"] != wantBytes {
					t.Errorf("Bedrock image bytes changed")
				}
			}
		}
		w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
		encoder := eventstream.NewEncoder()
		for _, item := range []struct{ name, payload string }{
			{"contentBlockDelta", `{"contentBlockIndex":0,"delta":{"text":"seen"}}`},
			{"messageStop", `{"stopReason":"end_turn"}`},
		} {
			message := eventstream.Message{Headers: eventstream.Headers{
				{Name: eventstream.EventTypeHeader, Value: eventstream.StringValue(item.name)},
				{Name: eventstream.MessageTypeHeader, Value: eventstream.StringValue(eventstream.EventMessageType)},
				{Name: eventstream.ContentTypeHeader, Value: eventstream.StringValue("application/json")},
			}, Payload: []byte(item.payload)}
			if err := encoder.Encode(w, message); err != nil {
				t.Error(err)
			}
		}
	}))
	defer endpoint.Close()
	model := modelcfg.Model{Provider: "bedrock", ID: "amazon.nova-lite-v1:0", AWSRegion: "us-east-1", AWSAccessID: "test-access", AWSSecret: "test-secret", BaseURL: endpoint.URL}
	message := ChatMessage{Role: "user", Content: "describe", Images: []Image{image, image}}
	turn, err := ModelTurn(context.Background(), model, []ChatMessage{message}, nil)
	if err != nil || turn.Text != "seen" || requests != 1 {
		t.Fatalf("Bedrock image turn: %+v, requests=%d, err=%v", turn, requests, err)
	}
	message.Images[0].Data = make([]byte, maxImageBytes+1)
	if _, err := ModelTurn(context.Background(), model, []ChatMessage{message}, nil); err == nil || !strings.Contains(err.Error(), "1 MiB") || requests != 1 {
		t.Fatalf("Bedrock accepted oversized image request: %v, requests=%d", err, requests)
	}
}

func TestBedrockConverseStreamToolContinuation(t *testing.T) {
	requests := 0
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method != http.MethodPost || !strings.Contains(r.URL.Path, "/converse-stream") || !strings.Contains(r.URL.Path, "us.anthropic.claude-haiku-4-5") || !strings.Contains(r.Header.Get("Authorization"), "Credential=test-access/") {
			t.Errorf("Bedrock request: %s %s, Authorization=%q", r.Method, r.URL, r.Header.Get("Authorization"))
		}
		var body struct {
			Messages []struct {
				Role    string           `json:"role"`
				Content []map[string]any `json:"content"`
			} `json:"messages"`
			Tools any `json:"toolConfig"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.Messages) == 0 || body.Tools == nil {
			t.Errorf("Bedrock body: %+v, err=%v", body, err)
		}
		if requests == 2 && (len(body.Messages) != 3 || body.Messages[1].Role != "assistant" || body.Messages[2].Role != "user" || body.Messages[2].Content[0]["toolResult"] == nil) {
			t.Errorf("Bedrock tool continuation: %+v", body.Messages)
		}
		w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
		encoder := eventstream.NewEncoder()
		send := func(name string, payload any) {
			encoded, err := json.Marshal(payload)
			if err != nil {
				t.Error(err)
				return
			}
			message := eventstream.Message{Headers: eventstream.Headers{
				{Name: eventstream.EventTypeHeader, Value: eventstream.StringValue(name)},
				{Name: eventstream.MessageTypeHeader, Value: eventstream.StringValue(eventstream.EventMessageType)},
				{Name: eventstream.ContentTypeHeader, Value: eventstream.StringValue("application/json")},
			}, Payload: encoded}
			if err := encoder.Encode(w, message); err != nil {
				t.Error(err)
			}
		}
		send("messageStart", map[string]any{"role": "assistant"})
		if requests == 1 {
			send("contentBlockStart", map[string]any{"contentBlockIndex": 0, "start": map[string]any{"toolUse": map[string]any{"toolUseId": "call_1", "name": "read_file"}}})
			send("contentBlockDelta", map[string]any{"contentBlockIndex": 0, "delta": map[string]any{"toolUse": map[string]any{"input": `{"path":"/tmp/a"}`}}})
			send("contentBlockStop", map[string]any{"contentBlockIndex": 0})
			send("messageStop", map[string]any{"stopReason": "tool_use"})
		} else {
			send("contentBlockDelta", map[string]any{"contentBlockIndex": 0, "delta": map[string]any{"text": "Bedrock reply"}})
			send("messageStop", map[string]any{"stopReason": "end_turn"})
			send("metadata", map[string]any{"usage": map[string]any{"inputTokens": 37, "outputTokens": 9, "totalTokens": 46, "cacheReadInputTokens": 5}, "metrics": map[string]any{"latencyMs": 10}})
		}
	}))
	defer endpoint.Close()
	model := modelcfg.Model{Provider: "bedrock", ID: "anthropic.claude-haiku-4-5", AWSRegion: "us-east-1", AWSAccessID: "test-access", AWSSecret: "test-secret", BaseURL: endpoint.URL}
	schemas := []map[string]any{{"type": "function", "function": map[string]any{"name": "read_file", "parameters": map[string]any{"type": "object"}}}}
	messages := []ChatMessage{{Role: "user", Content: "read"}}
	first, err := ModelTurn(context.Background(), model, messages, schemas)
	if err != nil || len(first.Calls) != 1 || first.Calls[0].ID != "call_1" || first.Calls[0].Function.Arguments != `{"path":"/tmp/a"}` || first.FinishReason != "tool_use" {
		t.Fatalf("Bedrock tool call: %+v, err=%v", first, err)
	}
	messages = append(messages, ChatMessage{Role: "assistant", ToolCalls: first.Calls}, ChatMessage{Role: "tool", ToolCallID: "call_1", Content: "file contents"})
	second, err := ModelTurn(context.Background(), model, messages, schemas)
	if err != nil || second.Text != "Bedrock reply" || second.FinishReason != "end_turn" || second.Usage == nil || *second.Usage != (ModelUsage{InputTokens: 37, OutputTokens: 9, CacheReadTokens: 5}) || requests != 2 {
		t.Fatalf("Bedrock continuation: %+v, requests=%d, err=%v", second, requests, err)
	}
}

func TestBedrockSignedReasoningContinuesWithTool(t *testing.T) {
	requests := 0
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		var body struct {
			Additional map[string]any `json:"additionalModelRequestFields"`
			Inference  struct {
				MaxTokens int `json:"maxTokens"`
			} `json:"inferenceConfig"`
			Messages []struct {
				Content []map[string]any `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		thinking, _ := body.Additional["thinking"].(map[string]any)
		if body.Inference.MaxTokens != 4096 || thinking["type"] != "enabled" || thinking["budget_tokens"] != float64(2048) {
			t.Errorf("Bedrock thinking request: %+v", body)
		}
		if requests == 2 {
			if len(body.Messages) != 3 || len(body.Messages[1].Content) != 2 {
				t.Errorf("Bedrock reasoning continuation: %+v", body.Messages)
			} else {
				reasoning, _ := body.Messages[1].Content[0]["reasoningContent"].(map[string]any)
				signed, _ := reasoning["reasoningText"].(map[string]any)
				if signed["text"] != "first thought" || signed["signature"] != "signed-token" || body.Messages[1].Content[1]["toolUse"] == nil {
					t.Errorf("Bedrock signed reasoning was not replayed before tool use: %+v", body.Messages[1].Content)
				}
			}
		}
		w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
		encoder := eventstream.NewEncoder()
		send := func(name string, payload any) {
			encoded, err := json.Marshal(payload)
			if err != nil {
				t.Error(err)
				return
			}
			message := eventstream.Message{Headers: eventstream.Headers{
				{Name: eventstream.EventTypeHeader, Value: eventstream.StringValue(name)},
				{Name: eventstream.MessageTypeHeader, Value: eventstream.StringValue(eventstream.EventMessageType)},
				{Name: eventstream.ContentTypeHeader, Value: eventstream.StringValue("application/json")},
			}, Payload: encoded}
			if err := encoder.Encode(w, message); err != nil {
				t.Error(err)
			}
		}
		if requests == 1 {
			send("contentBlockDelta", map[string]any{"contentBlockIndex": 0, "delta": map[string]any{"reasoningContent": map[string]any{"text": "first thought"}}})
			send("contentBlockDelta", map[string]any{"contentBlockIndex": 0, "delta": map[string]any{"reasoningContent": map[string]any{"signature": "signed-token"}}})
			send("contentBlockStop", map[string]any{"contentBlockIndex": 0})
			send("contentBlockStart", map[string]any{"contentBlockIndex": 1, "start": map[string]any{"toolUse": map[string]any{"toolUseId": "call_1", "name": "read_file"}}})
			send("contentBlockDelta", map[string]any{"contentBlockIndex": 1, "delta": map[string]any{"toolUse": map[string]any{"input": `{"path":"/work/a"}`}}})
			send("contentBlockStop", map[string]any{"contentBlockIndex": 1})
			send("messageStop", map[string]any{"stopReason": "tool_use"})
		} else {
			send("contentBlockDelta", map[string]any{"contentBlockIndex": 0, "delta": map[string]any{"text": "done"}})
			send("messageStop", map[string]any{"stopReason": "end_turn"})
		}
	}))
	defer endpoint.Close()
	requested := 4096
	model := modelcfg.Model{Provider: "bedrock", ID: "anthropic.claude-haiku-4-5", AWSRegion: "us-east-1", AWSAccessID: "test-access", AWSSecret: "test-secret", BaseURL: endpoint.URL, OutputLimit: 4096, Thinking: &modelcfg.ThinkingOption{Type: "enabled", BudgetTokens: &requested}}
	schemas := []map[string]any{{"type": "function", "function": map[string]any{"name": "read_file", "parameters": map[string]any{"type": "object"}}}}
	messages := []ChatMessage{{Role: "user", Content: "read"}}
	var visible strings.Builder
	first, err := ModelTurnStreamWithReasoning(context.Background(), model, messages, schemas, nil, func(delta string) error { _, err := visible.WriteString(delta); return err })
	if err != nil || len(first.Calls) != 1 || len(first.BedrockParts) != 2 || visible.String() != "first thought" {
		t.Fatalf("Bedrock reasoning and tool: %+v, visible=%q, err=%v", first, visible.String(), err)
	}
	messages = append(messages, ChatMessage{Role: "assistant", ToolCalls: first.Calls, BedrockParts: first.BedrockParts}, ChatMessage{Role: "tool", ToolCallID: "call_1", Content: "file contents"})
	second, err := ModelTurn(context.Background(), model, messages, schemas)
	if err != nil || second.Text != "done" || requests != 2 {
		t.Fatalf("Bedrock signed reasoning continuation: %+v, requests=%d, err=%v", second, requests, err)
	}
}

func TestBedrockReasoningBoundsAndReplay(t *testing.T) {
	parse := func(delta types.ContentBlockDelta, onReasoning func(string) error) (Turn, error) {
		events := make(chan types.ConverseStreamOutput, 3)
		events <- &types.ConverseStreamOutputMemberContentBlockDelta{Value: types.ContentBlockDeltaEvent{ContentBlockIndex: aws.Int32(0), Delta: delta}}
		events <- &types.ConverseStreamOutputMemberContentBlockStop{Value: types.ContentBlockStopEvent{ContentBlockIndex: aws.Int32(0)}}
		events <- &types.ConverseStreamOutputMemberMessageStop{Value: types.MessageStopEvent{StopReason: types.StopReasonEndTurn}}
		close(events)
		return parseBedrockEventsWithReasoning(events, func() error { return nil }, nil, onReasoning)
	}
	if _, err := parse(&types.ContentBlockDeltaMemberReasoningContent{Value: &types.ReasoningContentBlockDeltaMemberText{Value: strings.Repeat("x", 64<<10+1)}}, nil); err == nil || !strings.Contains(err.Error(), "64 KiB") {
		t.Fatalf("oversized Bedrock reasoning accepted: %v", err)
	}
	abort := fmt.Errorf("reasoning callback stopped")
	if _, err := parse(&types.ContentBlockDeltaMemberReasoningContent{Value: &types.ReasoningContentBlockDeltaMemberText{Value: "thought"}}, func(string) error { return abort }); err != abort {
		t.Fatalf("Bedrock reasoning callback error lost: %v", err)
	}
	turn, err := parse(&types.ContentBlockDeltaMemberReasoningContent{Value: &types.ReasoningContentBlockDeltaMemberRedactedContent{Value: []byte("opaque")}}, nil)
	if err != nil || len(turn.BedrockParts) != 1 || string(turn.BedrockParts[0].Redacted) != "opaque" {
		t.Fatalf("Bedrock redacted reasoning: %+v, err=%v", turn, err)
	}
	parts, err := bedrockReplayParts(turn.BedrockParts)
	if err != nil || len(parts) != 1 {
		t.Fatalf("Bedrock redacted replay: %+v, err=%v", parts, err)
	}
	block, ok := parts[0].(*types.ContentBlockMemberReasoningContent)
	if !ok {
		t.Fatalf("Bedrock redacted replay type: %T", parts[0])
	}
	redacted, ok := block.Value.(*types.ReasoningContentBlockMemberRedactedContent)
	if !ok || string(redacted.Value) != "opaque" {
		t.Fatalf("Bedrock redacted bytes changed: %+v", block.Value)
	}
	parts, err = bedrockReplayParts([]BedrockPart{{Kind: "reasoning", Text: "unsigned"}})
	if err != nil || len(parts) != 0 {
		t.Fatalf("Bedrock unsigned reasoning replay: %+v, err=%v", parts, err)
	}
}

func TestBedrockLimitsContentBlockCount(t *testing.T) {
	events := make(chan types.ConverseStreamOutput, 65)
	for index := int32(0); index < 65; index++ {
		events <- &types.ConverseStreamOutputMemberContentBlockDelta{Value: types.ContentBlockDeltaEvent{
			ContentBlockIndex: aws.Int32(index),
			Delta:             &types.ContentBlockDeltaMemberText{Value: "x"},
		}}
	}
	close(events)
	if _, err := parseBedrockEvents(events, func() error { return nil }, nil); err == nil || !strings.Contains(err.Error(), "content blocks exceed") {
		t.Fatalf("Bedrock accepted more than 64 content blocks: %v", err)
	}
}

func TestBedrockModelSpecificReasoningEffort(t *testing.T) {
	for _, check := range []struct {
		name, id, field string
	}{
		{"Claude", "anthropic.claude-haiku-4-5-20251001-v1:0", "output_config"},
		{"OpenAI Harmony", "openai.gpt-oss-120b-1:0", "reasoning_effort"},
		{"OpenAI Responses style", "openai.gpt-5.6-sol", "reasoning"},
		{"Nova", "amazon.nova-2-lite-v1:0", "reasoningConfig"},
	} {
		t.Run(check.name, func(t *testing.T) {
			calls := 0
			endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				var body struct {
					Additional map[string]any `json:"additionalModelRequestFields"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if len(body.Additional) != 1 {
					t.Errorf("Bedrock effort added unexpected fields: %+v", body.Additional)
				}
				switch check.field {
				case "output_config", "reasoning":
					value, _ := body.Additional[check.field].(map[string]any)
					if value["effort"] != "high" {
						t.Errorf("Bedrock effort shape: %+v", body.Additional)
					}
				case "reasoningConfig":
					value, _ := body.Additional[check.field].(map[string]any)
					if value["maxReasoningEffort"] != "high" {
						t.Errorf("Bedrock effort shape: %+v", body.Additional)
					}
				default:
					if body.Additional[check.field] != "high" {
						t.Errorf("Bedrock effort shape: %+v", body.Additional)
					}
				}
				w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
				encoder := eventstream.NewEncoder()
				for _, event := range []struct{ name, payload string }{
					{"contentBlockDelta", `{"contentBlockIndex":0,"delta":{"text":"ok"}}`},
					{"messageStop", `{"stopReason":"end_turn"}`},
				} {
					message := eventstream.Message{Headers: eventstream.Headers{
						{Name: eventstream.EventTypeHeader, Value: eventstream.StringValue(event.name)},
						{Name: eventstream.MessageTypeHeader, Value: eventstream.StringValue(eventstream.EventMessageType)},
						{Name: eventstream.ContentTypeHeader, Value: eventstream.StringValue("application/json")},
					}, Payload: []byte(event.payload)}
					if err := encoder.Encode(w, message); err != nil {
						t.Error(err)
					}
				}
			}))
			defer endpoint.Close()
			model := modelcfg.Model{Provider: "bedrock", ProviderID: "amazon-bedrock", ID: check.id, AWSRegion: "us-east-1", AWSAccessID: "test-access", AWSSecret: "test-secret", BaseURL: endpoint.URL, ReasoningEffort: "high"}
			turn, err := ModelTurn(context.Background(), model, []ChatMessage{{Role: "user", Content: "hello"}}, nil)
			if err != nil || turn.Text != "ok" || calls != 1 {
				t.Fatalf("Bedrock effort turn: %+v, calls=%d, err=%v", turn, calls, err)
			}
		})
	}
}

func TestBedrockRejectsInvalidPostStopUsage(t *testing.T) {
	events := make(chan types.ConverseStreamOutput, 2)
	events <- &types.ConverseStreamOutputMemberMessageStop{Value: types.MessageStopEvent{StopReason: types.StopReasonEndTurn}}
	events <- &types.ConverseStreamOutputMemberMetadata{Value: types.ConverseStreamMetadataEvent{Usage: &types.TokenUsage{InputTokens: aws.Int32(-1), OutputTokens: aws.Int32(4)}}}
	close(events)
	if _, err := parseBedrockEvents(events, func() error { return nil }, nil); err == nil || !strings.Contains(err.Error(), "invalid token usage") {
		t.Fatalf("invalid Bedrock usage was accepted: %v", err)
	}
}

func TestBedrockWireModelID(t *testing.T) {
	for _, check := range []struct{ id, region, want string }{
		{"anthropic.claude-haiku-4-5", "us-east-1", "us.anthropic.claude-haiku-4-5"},
		{"us.anthropic.claude-haiku-4-5", "us-east-1", "us.anthropic.claude-haiku-4-5"},
		{"anthropic.claude-haiku-4-5", "ap-northeast-1", "jp.anthropic.claude-haiku-4-5"},
		{"anthropic.claude-haiku-4-5", "eu-west-1", "eu.anthropic.claude-haiku-4-5"},
		{"arn:aws:bedrock:us-east-1:123:inference-profile/x", "us-east-1", "arn:aws:bedrock:us-east-1:123:inference-profile/x"},
	} {
		if got := bedrockWireModelID(check.id, check.region); got != check.want {
			t.Fatal(fmt.Sprintf("%q in %q -> %q, want %q", check.id, check.region, got, check.want))
		}
	}
}

func TestBedrockFrameLimitBeforeSDKDecode(t *testing.T) {
	var prefix [4]byte
	binary.BigEndian.PutUint32(prefix[:], maxBedrockFrameBytes+1)
	input := io.NopCloser(bytes.NewReader(prefix[:]))
	reader := &limitedBedrockBody{source: input, reader: bufio.NewReader(input)}
	var out [16]byte
	if _, err := reader.Read(out[:]); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized Bedrock frame passed to SDK: %v", err)
	}
}

func TestBedrockTransportSendsLargeRequest(t *testing.T) {
	requests := 0
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		read, err := io.Copy(io.Discard, r.Body)
		if err != nil || read != 1<<20 {
			t.Errorf("request bytes=%d err=%v", read, err)
		}
	}))
	defer endpoint.Close()
	request, err := http.NewRequest(http.MethodPost, endpoint.URL, bytes.NewReader(make([]byte, 1<<20)))
	if err != nil {
		t.Fatal(err)
	}
	response, err := (bedrockTransport{base: endpoint.Client().Transport}).RoundTrip(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if requests != 1 {
		t.Fatalf("large Bedrock request was not sent: %d", requests)
	}
}

func TestBedrockBearerAuthentication(t *testing.T) {
	endpoint := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer private-token" {
			t.Errorf("Bedrock bearer auth: %q", got)
		}
		w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
		encoder := eventstream.NewEncoder()
		for _, item := range []struct{ name, payload string }{
			{"contentBlockDelta", `{"contentBlockIndex":0,"delta":{"text":"bearer reply"}}`},
			{"messageStop", `{"stopReason":"end_turn"}`},
		} {
			message := eventstream.Message{Headers: eventstream.Headers{
				{Name: eventstream.EventTypeHeader, Value: eventstream.StringValue(item.name)},
				{Name: eventstream.MessageTypeHeader, Value: eventstream.StringValue(eventstream.EventMessageType)},
				{Name: eventstream.ContentTypeHeader, Value: eventstream.StringValue("application/json")},
			}, Payload: []byte(item.payload)}
			if err := encoder.Encode(w, message); err != nil {
				t.Error(err)
			}
		}
	}))
	defer endpoint.Close()
	previous := bedrockHTTPClient.Transport
	bedrockHTTPClient.Transport = bedrockTransport{base: endpoint.Client().Transport}
	defer func() { bedrockHTTPClient.Transport = previous }()
	model := modelcfg.Model{Provider: "bedrock", ID: "amazon.nova-micro-v1:0", AWSRegion: "us-east-1", APIKey: "private-token", BaseURL: endpoint.URL}
	turn, err := ModelTurn(context.Background(), model, []ChatMessage{{Role: "user", Content: "hello"}}, nil)
	if err != nil || turn.Text != "bearer reply" {
		t.Fatalf("Bedrock bearer turn: %+v, err=%v", turn, err)
	}
}
