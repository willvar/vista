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
	command := workerproto.Command{RunID: "latency-run", Prompt: "Hi", Model: modelcfg.Model{ID: "latency-test", Provider: "openai-chat", BaseURL: server.URL, APIKey: "test"}}
	_, err := Run(context.Background(), command, func(r workerproto.Result) error {
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
