package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	modelcfg "vista/internal/model"
)

func TestFailedModelTurnsRetainTerminationDiagnostics(t *testing.T) {
	for _, test := range []struct {
		name, protocol, stream, reason string
	}{
		{
			"chat length", "openai-chat",
			`data: {"choices":[{"delta":{"content":"partial 中文"},"finish_reason":"length"}],"usage":{"prompt_tokens":100,"completion_tokens":25}}` + "\n\n" +
				`data: [DONE]` + "\n\n",
			"length",
		},
		{
			"chat filter", "openai-chat",
			`data: {"choices":[{"delta":{"content":"partial 中文"},"finish_reason":"content_filter"}],"usage":{"prompt_tokens":100,"completion_tokens":25}}` + "\n\n" +
				`data: [DONE]` + "\n\n",
			"content_filter",
		},
		{
			"responses delta", "openai-responses",
			`data: {"type":"response.output_text.delta","item_id":"message","delta":"partial 中文"}` + "\n\n" +
				`data: {"type":"response.incomplete","response":{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"usage":{"input_tokens":100,"output_tokens":25}}}` + "\n\n",
			"max_output_tokens",
		},
		{
			"responses terminal text", "openai-responses",
			`data: {"type":"response.incomplete","response":{"status":"incomplete","incomplete_details":{"reason":"content_filter"},"output":[{"type":"message","content":[{"type":"output_text","text":"partial 中文"}]}],"usage":{"input_tokens":100,"output_tokens":25}}}` + "\n\n",
			"content_filter",
		},
		{
			"responses failed", "openai-responses",
			`data: {"type":"response.output_text.delta","item_id":"message","delta":"partial 中文"}` + "\n\n" +
				`data: {"type":"response.failed","response":{"status":"failed","error":{"message":"upstream failed"},"usage":{"input_tokens":100,"output_tokens":25}}}` + "\n\n",
			"failed",
		},
		{
			"responses item text", "openai-responses",
			`data: {"type":"response.output_item.done","item":{"id":"message","type":"message","content":[{"type":"output_text","text":"partial 中文"}]}}` + "\n\n" +
				`data: {"type":"response.incomplete","response":{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"usage":{"input_tokens":100,"output_tokens":25}}}` + "\n\n",
			"max_output_tokens",
		},
		{
			"gemini limit", "gemini",
			`data: {"candidates":[{"content":{"parts":[{"text":"partial 中文"}]},"finishReason":"MAX_TOKENS"}],"usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":25}}` + "\n\n",
			"MAX_TOKENS",
		},
		{
			"gemini safety", "gemini",
			`data: {"candidates":[{"content":{"parts":[{"text":"partial 中文"}]},"finishReason":"SAFETY"}],"usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":25}}` + "\n\n",
			"SAFETY",
		},
		{
			"gemini prompt safety", "gemini",
			`data: {"candidates":[{"content":{"parts":[{"text":"partial 中文"}]}}],"usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":25}}` + "\n\n" +
				`data: {"promptFeedback":{"blockReason":"SAFETY"}}` + "\n\n",
			"SAFETY",
		},
		{
			"anthropic limit", "anthropic",
			`data: {"type":"message_start","message":{"usage":{"input_tokens":100,"output_tokens":0}}}` + "\n\n" +
				`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"partial 中文"}}` + "\n\n" +
				`data: {"type":"message_delta","delta":{"stop_reason":"max_tokens"},"usage":{"output_tokens":25}}` + "\n\n" +
				`data: {"type":"message_stop"}` + "\n\n",
			"max_tokens",
		},
		{
			"mistral limit", "mistral",
			`data: {"choices":[{"delta":{"content":"partial 中文"},"finish_reason":"length"}],"usage":{"prompt_tokens":100,"completion_tokens":25}}` + "\n\n" +
				`data: [DONE]` + "\n\n",
			"length",
		},
		{
			"cohere limit", "cohere",
			`data: {"type":"content-delta","delta":{"message":{"content":{"text":"partial 中文"}}}}` + "\n\n" +
				`data: {"type":"message-end","delta":{"finish_reason":"MAX_TOKENS","usage":{"tokens":{"input_tokens":100,"output_tokens":25}}}}` + "\n\n",
			"MAX_TOKENS",
		},
		{
			"gateway limit", "vercel-gateway",
			`data: {"type":"text-delta","id":"text","delta":"partial 中文"}` + "\n\n" +
				`data: {"type":"finish","finishReason":{"unified":"length"},"usage":{"inputTokens":{"total":100},"outputTokens":{"total":25}}}` + "\n\n",
			"length",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			requests := 0
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				requests++
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, test.stream)
			}))
			defer upstream.Close()
			turn, err := ModelTurn(context.Background(), modelcfg.Model{Provider: test.protocol, ID: "fixture", APIKey: "fixture", BaseURL: upstream.URL}, []ChatMessage{{Role: "user", Content: "hello"}}, nil)
			if err == nil || requests != 1 || turn.StoppedNormally() || len(turn.Calls) != 0 {
				t.Fatalf("abnormal turn completed or retried: turn=%+v requests=%d err=%v", turn, requests, err)
			}
			if len(turn.ResponseOutput)+len(turn.ResponseReasoning)+len(turn.AnthropicBlocks)+len(turn.BedrockParts)+len(turn.MistralParts) != 0 {
				t.Fatalf("failed turn returned native replay state: %+v", turn)
			}
			if turn.Text != "partial 中文" || turn.FinishReason != test.reason || turn.Usage == nil || turn.Usage.InputTokens != 100 || turn.Usage.OutputTokens != 25 {
				t.Fatalf("lost failure diagnostics: turn=%+v reason=%q err=%v", turn, test.reason, err)
			}
		})
	}
}

func TestBedrockFailureRetainsTerminationDiagnostics(t *testing.T) {
	events := make(chan types.ConverseStreamOutput, 4)
	events <- &types.ConverseStreamOutputMemberContentBlockDelta{Value: types.ContentBlockDeltaEvent{ContentBlockIndex: aws.Int32(0), Delta: &types.ContentBlockDeltaMemberText{Value: "partial 中文"}}}
	events <- &types.ConverseStreamOutputMemberContentBlockStop{Value: types.ContentBlockStopEvent{ContentBlockIndex: aws.Int32(0)}}
	events <- &types.ConverseStreamOutputMemberMetadata{Value: types.ConverseStreamMetadataEvent{Usage: &types.TokenUsage{InputTokens: aws.Int32(100), OutputTokens: aws.Int32(25)}}}
	events <- &types.ConverseStreamOutputMemberMessageStop{Value: types.MessageStopEvent{StopReason: types.StopReasonMaxTokens}}
	close(events)
	turn, err := parseBedrockEvents(events, func() error { return nil }, nil)
	if err == nil || turn.StoppedNormally() || turn.Text != "partial 中文" || turn.FinishReason != "max_tokens" || turn.Usage == nil || turn.Usage.InputTokens != 100 || turn.Usage.OutputTokens != 25 {
		t.Fatalf("lost Bedrock failure diagnostics: turn=%+v err=%v", turn, err)
	}
}
