package provider

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"github.com/google/uuid"
	modelcfg "vista/internal/model"
)

func TestUsageRecorderCoversPurposesFailuresAndMissingUsageOnce(t *testing.T) {
	for _, scenario := range []string{"conversation", "compaction", "title", "missing", "failed"} {
		t.Run(scenario, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if scenario == "failed" {
					http.Error(w, "unavailable", 503)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"answer\"},\"finish_reason\":\"stop\"}]}\n\n")
				if scenario != "missing" {
					fmt.Fprint(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":100,\"completion_tokens\":25,\"prompt_tokens_details\":{\"cached_tokens\":50},\"completion_tokens_details\":{\"reasoning_tokens\":10}}}\n\n")
				}
				fmt.Fprint(w, "data: [DONE]\n\n")
			}))
			defer server.Close()
			calls := 0
			ctx := WithUsageRecorder(context.Background(), func(id string, tokens *int64) {
				calls++
				if _, err := uuid.Parse(id); err != nil {
					t.Fatal(err)
				}
				if scenario == "missing" {
					if tokens != nil {
						t.Fatal("invented missing usage", *tokens)
					}
					return
				}
				if tokens == nil || *tokens != 125 {
					t.Fatal("duplicate cache/reasoning or missing usage", tokens)
				}
			})
			model := modelcfg.Model{Provider: "openai-chat", ID: "test", APIKey: "test", BaseURL: server.URL}
			if scenario == "title" {
				model = ForPurpose(model, PurposeTitle)
			}
			if scenario == "compaction" {
				model = ForPurpose(model, PurposeCompaction)
			}
			_, err := ModelTurn(ctx, model, []ChatMessage{{Role: "user", Content: "hello"}}, nil)
			wantCalls := 1
			if scenario == "failed" {
				wantCalls = 0
			}
			if (err != nil) != (scenario == "failed") || calls != wantCalls {
				t.Fatal("wrong collection count", calls, err)
			}
		})
	}
}

func TestUsageRecorderOnlyCountsUsableFailedUsage(t *testing.T) {
	for _, requestErr := range []error{nil, errors.New("upstream failed"), context.Canceled} {
		for _, usage := range []*ModelUsage{nil, {}, {InputTokens: 100, OutputTokens: 25, CacheReadTokens: 50, ReasoningTokens: 10}, {InputTokens: -1}, {OutputTokens: 1_000_000_001}} {
			calls := 0
			ctx := WithUsageRecorder(context.Background(), func(id string, tokens *int64) {
				calls++
				if _, err := uuid.Parse(id); err != nil {
					t.Fatal(err)
				}
				if usage == nil || !validModelUsage(*usage) {
					if tokens != nil {
						t.Fatal("invented usage", tokens)
					}
				} else if tokens == nil || *tokens != usage.InputTokens+usage.OutputTokens {
					t.Fatal("lost usage or double-counted subsets", tokens)
				}
			})
			recordUsage(ctx, usage, requestErr)
			want := 1
			if requestErr != nil && (usage == nil || !validModelUsage(*usage)) {
				want = 0
			}
			if calls != want {
				t.Fatalf("usage=%+v error=%v: recorded %d times, want %d", usage, requestErr, calls, want)
			}
		}
	}
}

func TestModelFailuresRetainReportedUsageWithoutCompletedOutput(t *testing.T) {
	for _, scenario := range []struct {
		protocol string
		stream   string
	}{
		{"openai-chat", `data: {"choices":[{"delta":{"content":"partial"}}],"usage":{"prompt_tokens":100,"completion_tokens":25}}` + "\n\ndata: {\"error\":{\"message\":\"unavailable\"}}\n\n"},
		{"openai-responses", `data: {"type":"response.failed","response":{"status":"failed","usage":{"input_tokens":100,"output_tokens":25},"error":{"message":"unavailable"}}}` + "\n\n"},
		{"anthropic", `data: {"type":"message_start","message":{"usage":{"input_tokens":100,"output_tokens":0}}}` + "\n\n" + `data: {"type":"message_delta","delta":{},"usage":{"output_tokens":25}}` + "\n\n" + `data: {"type":"error","error":{"message":"unavailable"}}` + "\n\n"},
		{"gemini", `data: {"candidates":[{"content":{"parts":[{"text":"partial"}]}}],"usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":25}}` + "\n\n"},
		{"mistral", `data: {"choices":[{"delta":{"content":"partial"}}],"usage":{"prompt_tokens":100,"completion_tokens":25}}` + "\n\n"},
		{"cohere", `data: {"type":"message-end","delta":{"finish_reason":"MAX_TOKENS","usage":{"tokens":{"input_tokens":100,"output_tokens":25}}}}` + "\n\n"},
		{"vercel-gateway", `data: {"type":"finish","finishReason":{"unified":"length"},"usage":{"inputTokens":{"total":100},"outputTokens":{"total":25}}}` + "\n\n"},
	} {
		t.Run(scenario.protocol, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, scenario.stream)
			}))
			defer server.Close()
			calls := 0
			ctx := WithUsageRecorder(context.Background(), func(_ string, tokens *int64) {
				calls++
				if tokens == nil || *tokens != 125 {
					t.Fatal("lost usage reported before failure", tokens)
				}
			})
			model := modelcfg.Model{Provider: scenario.protocol, ID: "test", APIKey: "test", BaseURL: server.URL}
			turn, err := ModelTurn(ctx, model, []ChatMessage{{Role: "user", Content: "hello"}}, nil)
			if err == nil || calls != 1 || turn.Usage == nil || turn.Text != "" || len(turn.Calls) != 0 {
				t.Fatalf("failed output or usage mishandled: %+v calls=%d err=%v", turn, calls, err)
			}
		})
	}
}

type usageReadFailure struct{ err error }

func (r usageReadFailure) Read([]byte) (int, error) { return 0, r.err }

func TestStreamReadFailureAndCancellationRetainUsage(t *testing.T) {
	for _, readErr := range []error{errors.New("connection lost"), context.Canceled} {
		stream := `data: {"choices":[],"usage":{"prompt_tokens":100,"completion_tokens":25}}` + "\n\n"
		turn, err := parseMistralStreamWithReasoning(io.MultiReader(strings.NewReader(stream), usageReadFailure{readErr}), nil, nil)
		if !errors.Is(err, readErr) || turn.Usage == nil || turn.Usage.InputTokens != 100 || turn.Usage.OutputTokens != 25 || turn.Text != "" {
			t.Fatal("lost usage on read failure", turn, err)
		}
	}
	events := make(chan types.ConverseStreamOutput, 1)
	events <- &types.ConverseStreamOutputMemberMetadata{Value: types.ConverseStreamMetadataEvent{Usage: &types.TokenUsage{InputTokens: aws.Int32(100), OutputTokens: aws.Int32(25)}}}
	close(events)
	turn, err := parseBedrockEvents(events, func() error { return context.Canceled }, nil)
	if !errors.Is(err, context.Canceled) || turn.Usage == nil || turn.Usage.InputTokens != 100 || turn.Usage.OutputTokens != 25 || turn.Text != "" {
		t.Fatal("lost Bedrock usage on cancellation", turn, err)
	}
}
