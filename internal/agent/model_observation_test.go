package agent

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	modelcfg "vista/internal/model"
	"vista/internal/provider"
	"vista/internal/workerproto"
)

func TestFirstTextLatencyDoesNotStopAtReasoningOrWhitespace(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"choices":[{"delta":{"reasoning_content":"thinking","content":" "}}]}`+"\n\n")
		w.(http.Flusher).Flush()
		time.Sleep(60 * time.Millisecond)
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"Hello"},"finish_reason":"stop"}]}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()
	var observations []workerproto.ModelObservation
	var result *workerproto.ModelResult
	command := workerproto.Command{RunID: "latency-run", Prompt: "Hi", Model: modelcfg.Model{ID: "latency-test", Provider: "openai-chat", BaseURL: server.URL, APIKey: "test"}}
	_, err := Run(context.Background(), command, func(r workerproto.Result) error {
		if r.ModelResult != nil {
			result = r.ModelResult
		}
		if r.Observation != nil {
			observations = append(observations, *r.Observation)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(observations) != 1 || observations[0].FirstTextMS == nil || *observations[0].FirstTextMS < 50 {
		t.Fatalf("first text prematurely observed: %+v", observations)
	}
	if result == nil || result.FirstTokenMS == nil || *result.FirstTokenMS < 0 || *result.FirstTokenMS >= *observations[0].FirstTextMS || *result.FirstTokenMS > result.DurationMS {
		t.Fatalf("first token must include earlier reasoning without changing first-text selection timing: %+v", result)
	}
	if observations[0].Purpose != "conversation" || observations[0].InputTokens <= 0 || len(observations[0].Endpoint) != 64 {
		t.Fatalf("missing observation metadata: %+v", observations[0])
	}
}

func TestModelObservationRecordsMissingTextAndSkipsSubagents(t *testing.T) {
	command := workerproto.Command{RunID: "run", Model: modelcfg.Model{ID: "model"}}
	var results []workerproto.Result
	emit := func(r workerproto.Result) error { results = append(results, r); return nil }
	if err := observeModelRequest(command, 1, 1, 50, nil, provider.Turn{}, nil, emit); err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Fatal("subagent counted as main conversation")
	}
	if err := observeModelRequest(command, 2, 0, 50, nil, provider.Turn{}, nil, emit); err != nil {
		t.Fatal(err)
	}
	if results[0].Observation.FirstTextMS != nil || results[0].Observation.Outcome != "no_text" {
		t.Fatal("no text treated as zero latency")
	}
	if err := observeModelRequest(command, 3, 0, 50, nil, provider.Turn{}, context.Canceled, emit); err != nil {
		t.Fatal(err)
	}
	if results[1].Observation.Outcome != "cancelled" {
		t.Fatal("cancelled observation lost")
	}
}

func TestFirstTokenTimingWithoutBodyOrUsage(t *testing.T) {
	for _, tc := range []struct {
		name, delta string
		measured    bool
	}{
		{"no output", `{}`, false},
		{"reasoning only", `{"reasoning_content":"Thinking"}`, true},
		{"whitespace", `{"content":" "}`, true},
		{"body", `{"content":"Hello"}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":%s,\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n", tc.delta)
			}))
			defer server.Close()
			command := workerproto.Command{RunID: "run", Prompt: "Hi", Model: modelcfg.Model{ID: "model", Provider: "openai-chat", BaseURL: server.URL, APIKey: "test"}}
			var result *workerproto.ModelResult
			_, err := Run(context.Background(), command, func(r workerproto.Result) error {
				if r.ModelResult != nil {
					result = r.ModelResult
				}
				return nil
			})
			if err != nil || result == nil || result.Usage != nil || (result.FirstTokenMS != nil) != tc.measured {
				t.Fatalf("first-token result: %+v, error=%v", result, err)
			}
			if result.FirstTokenMS != nil && (*result.FirstTokenMS < 0 || *result.FirstTokenMS > result.DurationMS) {
				t.Fatalf("first token outside request duration: %+v", result)
			}
		})
	}
}
