package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	modelcfg "vista/internal/model"
	"vista/internal/workerproto"
)

func TestSubagentsShareModelAndRunConcurrently(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	tasks := []string{"alpha", "beta", "gamma", "delta", "epsilon", "zeta"}
	var children atomic.Int32
	var ready sync.Once
	allStarted := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Model    string `json:"model"`
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
			Tools []any `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if len(request.Messages) < 2 {
			t.Error("missing task messages")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if request.Model != "inherited-model" || len(request.Tools) < 9 {
			t.Errorf("subagent did not inherit model/tools: %+v", request)
		}
		user := request.Messages[1].Content
		var event any
		if user == "main" && request.Messages[len(request.Messages)-1].Role == "user" {
			calls := make([]any, 0, len(tasks))
			for index, task := range tasks {
				arguments, _ := json.Marshal(map[string]string{"task": task})
				calls = append(calls, map[string]any{"index": index, "id": task, "type": "function", "function": map[string]string{"name": "delegate", "arguments": string(arguments)}})
			}
			event = map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": calls}, "finish_reason": "tool_calls"}}}
		} else if user != "main" {
			if children.Add(1) == int32(len(tasks)) {
				ready.Do(func() { close(allStarted) })
			}
			select {
			case <-allStarted:
			case <-r.Context().Done():
				return
			case <-time.After(3 * time.Second):
				t.Error("subagents did not run concurrently")
			}
			event = map[string]any{"choices": []any{map[string]any{"delta": map[string]string{"content": user + " done"}, "finish_reason": "stop"}}}
		} else {
			var last delegationResult
			_ = json.Unmarshal([]byte(request.Messages[len(request.Messages)-1].Content), &last)
			if last.Status == "running" {
				calls := make([]any, 0, len(tasks))
				for index, task := range tasks {
					arguments, _ := json.Marshal(map[string]any{"taskIds": []string{"main/" + task}})
					calls = append(calls, map[string]any{"index": index, "id": "await-" + task, "type": "function", "function": map[string]string{"name": "await_agent", "arguments": string(arguments)}})
				}
				continuationEvent(w, map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": calls}, "finish_reason": "tool_calls"}}})
				return
			}
			for _, task := range tasks {
				found := false
				for _, message := range request.Messages {
					found = found || message.Role == "tool" && strings.Contains(message.Content, task+" done")
				}
				if !found {
					t.Errorf("child answer missing from parent: %s", task)
				}
			}
			event = map[string]any{"choices": []any{map[string]any{"delta": map[string]string{"content": "all complete"}, "finish_reason": "stop"}}}
		}
		encoded, _ := json.Marshal(event)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", encoded)
	}))
	defer server.Close()
	command := workerproto.Command{RunID: "parent-run", Prompt: "main", Model: modelcfg.Model{Provider: "openai-chat", ID: "inherited-model", BaseURL: server.URL, APIKey: "key"}}
	var events []workerproto.Result
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	answer, err := Run(ctx, command, func(event workerproto.Result) error { events = append(events, event); return nil })
	var results []workerproto.ModelResult
	events, results = splitModelResults(events)
	deltas := 0
	delegates := 0
	for _, event := range events {
		if event.Kind == "model_delta" {
			deltas++
		}
		if event.Kind == "tool" && event.Status != "started" {
			if event.Status != "succeeded" {
				t.Errorf("delegate failed: %s", event.Content)
			}
			if event.Tool.Name == "delegate" {
				delegates++
			}
		}
	}
	if err != nil || answer != "all complete" || children.Load() != int32(len(tasks)) || delegates != len(tasks) || len(results) != len(tasks)+3 || deltas != len(tasks)+1 {
		t.Fatalf("subagents: answer=%q err=%v children=%d events=%+v", answer, err, children.Load(), events)
	}
}
