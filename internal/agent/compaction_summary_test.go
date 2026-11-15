package agent

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
	"vista/internal/provider"
	"vista/internal/workerproto"
)

func TestFailedSummaryRetainsTerminationDiagnosticsAndContext(t *testing.T) {
	for _, test := range []struct {
		protocol, reason, validation string
		terminalOnly                 bool
	}{
		{"gemini", "MAX_TOKENS", "truncated", false},
		{"gemini", "SAFETY", "incomplete", false},
		{"openai-responses", "max_output_tokens", "truncated", false},
		{"openai-responses", "max_output_tokens", "truncated", true},
		{"openai-responses", "content_filter", "incomplete", true},
		{"openai-responses", "failed", "request_error", true},
		{"openai-responses", "model_context_window_exceeded", "context_overflow", false},
	} {
		t.Run(fmt.Sprintf("%s/%s/terminalOnly=%v", test.protocol, test.reason, test.terminalOnly), func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			const partial = "Incomplete checkpoint 中文"
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				if test.protocol == "gemini" {
					fmt.Fprintf(w, "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":%q}]},\"finishReason\":%q}],\"usageMetadata\":{\"promptTokenCount\":100,\"candidatesTokenCount\":25}}\n\n", partial, test.reason)
					return
				}
				if !test.terminalOnly {
					fmt.Fprintf(w, "data: {\"type\":\"response.output_text.delta\",\"item_id\":\"message\",\"delta\":%q}\n\n", partial)
				}
				if test.reason == "failed" {
					fmt.Fprintf(w, "data: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\",\"error\":{\"code\":\"server_error\",\"message\":\"provider unavailable\"},\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":%q}]}],\"usage\":{\"input_tokens\":100,\"output_tokens\":25}}}\n\n", partial)
					return
				}
				fmt.Fprintf(w, "data: {\"type\":\"response.incomplete\",\"response\":{\"status\":\"incomplete\",\"incomplete_details\":{\"reason\":%q},\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":%q}]}],\"usage\":{\"input_tokens\":100,\"output_tokens\":25}}}\n\n", test.reason, partial)
			}))
			defer upstream.Close()
			command := workerproto.Command{Action: "compact", RunID: "failed-summary", Model: modelcfg.Model{Provider: test.protocol, ID: "fixture", BaseURL: upstream.URL, APIKey: "fixture"}}
			messages := []provider.ChatMessage{{Role: "system", Content: "instructions"}, {Role: "user", Content: "task"}, {Role: "assistant", Content: strings.Repeat("evidence ", 2000)}}
			c := newExecutionCompactor(messages)
			var outcome *workerproto.ModelResult
			next, changed, err := c.compact(context.Background(), command, messages, nil, func(r workerproto.Result) error {
				if r.Kind == "model_delta" || r.Kind == "context_notice" || r.Kind == "model_history" || strings.Contains(r.Content, partial) {
					t.Errorf("failed candidate emitted: %+v", r)
				}
				if r.Kind == "model_result" {
					outcome = r.ModelResult
					encoded, marshalErr := json.Marshal(r)
					if marshalErr != nil || strings.Contains(string(encoded), partial) {
						t.Errorf("diagnostic captured candidate body: %s err=%v", encoded, marshalErr)
					}
				}
				return nil
			}, &runScope{}, 0)
			if err == nil || changed || !reflect.DeepEqual(next, messages) || c.summary != "" || outcome == nil || outcome.Summary == nil {
				t.Fatalf("failed summary changed context or lost outcome: changed=%v outcome=%+v err=%v", changed, outcome, err)
			}
			if outcome.FinishReason != "failed" || outcome.Summary.Validation != test.validation || outcome.Summary.UpstreamFinishReason != test.reason || outcome.Summary.TextBytes != len(partial) || outcome.Summary.EstimatedTokens != provider.EstimateTextTokens(partial) || outcome.Usage == nil || outcome.Usage.InputTokens != 100 || outcome.Usage.OutputTokens != 25 {
				t.Fatalf("wrong summary failure metadata: outcome=%+v summary=%+v err=%v", outcome, outcome.Summary, err)
			}
		})
	}
}

func TestSummaryFailureClassificationUsesStructuredReasons(t *testing.T) {
	for _, test := range []struct {
		reason, validation string
		err                error
	}{
		{"MAX_TOKENS", "truncated", errors.New("provider wording changed")},
		{"SAFETY", "incomplete", errors.New("provider wording changed")},
		{"", "request_error", errors.New("incomplete: max_output_tokens is only diagnostic text")},
		{"STOP", "request_error", errors.New("stream lost after finish")},
		{"failed", "request_error", &provider.ModelHTTPError{Code: "server_error"}},
		{"MAX_TOKENS", "cancelled", context.Canceled},
		{"MAX_TOKENS", "context_overflow", &provider.ModelHTTPError{Code: "context_length_exceeded"}},
	} {
		kind, err := validateCompactionSummary(provider.Turn{Text: "partial", FinishReason: test.reason}, test.err)
		if kind != test.validation || !errors.Is(err, test.err) {
			t.Errorf("reason=%q: validation=%q want=%q error=%v", test.reason, kind, test.validation, err)
		}
	}
}
