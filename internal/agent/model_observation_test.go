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

func TestFirstTokenTimingIsSharedWithConnectionUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"choices":[{"delta":{"reasoning_content":"thinking","content":" "}}]}`+"\n\n")
		w.(http.Flusher).Flush()
		time.Sleep(60 * time.Millisecond)
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"Hello"},"finish_reason":"stop"}]}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()
	var observations []modelcfg.CallUsage
	var result *workerproto.ModelResult
	command := workerproto.Command{RunID: "latency-run", Prompt: "Hi", Model: modelcfg.Model{ID: "latency-test", ConnectionID: "connection", Provider: "openai-chat", BaseURL: server.URL, APIKey: "test"}}
	ctx := provider.WithCallUsageRecorder(context.Background(), func(sample modelcfg.CallUsage) { observations = append(observations, sample) })
	_, err := Run(ctx, command, func(r workerproto.Result) error {
		if r.ModelResult != nil {
			result = r.ModelResult
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(observations) != 1 || observations[0].FirstTokenMS == nil || result == nil || result.FirstTokenMS == nil || *observations[0].FirstTokenMS != *result.FirstTokenMS || *result.FirstTokenMS >= result.DurationMS {
		t.Fatalf("statistics must reuse the reply's first-token sample: %+v %+v", observations, result)
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
