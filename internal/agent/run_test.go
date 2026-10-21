package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	modelcfg "vista/internal/model"
	"vista/internal/workerproto"
)

func TestAnswerPreviewFitsStoredEventWithEscapedText(t *testing.T) {
	for _, full := range []string{strings.Repeat("<", 8<<10), strings.Repeat("\x00", 8<<10), strings.Repeat("🙂", 64<<10)} {
		preview := answerPreview(full, "run-123", 1)
		if !strings.Contains(preview, "[Full answer:") || !strings.HasPrefix(full, strings.SplitN(preview, "\n\n[Full answer:", 2)[0]) {
			t.Fatalf("preview lost the output reference or text prefix: %q", preview[:min(len(preview), 100)])
		}
		payload, err := json.Marshal(map[string]any{"runId": "run-123", "status": "succeeded", "message": map[string]any{"id": "message-123", "sessionId": "session-123", "runId": "run-123", "sequence": 123, "role": "assistant", "content": preview, "createdAt": "2026-09-28T08:00:00Z", "provider": "openai", "modelId": "gpt-6-sol", "outputTurn": 1, "outputBytes": len(full)}})
		if err != nil || len(payload) > 32<<10 {
			t.Fatalf("run_finished preview exceeds event bound: %d bytes, err=%v", len(payload), err)
		}
	}
}

func TestAgentReadsFileThroughModelToolCall(t *testing.T) {
	path := filepath.Join(t.TempDir(), "note.txt")
	if err := os.WriteFile(path, []byte("before\nhello from disk\nafter"), 0600); err != nil {
		t.Fatal(err)
	}
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		var body struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
			Tools []struct {
				Function struct {
					Name       string
					Parameters struct{ Properties map[string]any }
				} `json:"function"`
			} `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		readOffered := false
		for _, tool := range body.Tools {
			readOffered = readOffered || tool.Function.Name == "read_file"
			if tool.Function.Name == "read_file" {
				properties := tool.Function.Parameters.Properties
				if properties["startLine"] == nil || properties["lineCount"] == nil || properties["offset"] != nil || properties["limit"] != nil {
					t.Errorf("model was not given the line-based file schema: %+v", properties)
				}
			}
		}
		if !readOffered {
			t.Error("read_file was not offered to the model")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if requests == 1 {
			args, _ := json.Marshal(map[string]any{"path": path, "startLine": 2, "lineCount": 1})
			chunk, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "call_1", "type": "function", "function": map[string]string{"name": "read_file", "arguments": string(args)}}}}, "finish_reason": "tool_calls"}}})
			fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", chunk)
			return
		}
		if body.Messages[len(body.Messages)-1].Role != "tool" || !strings.Contains(body.Messages[len(body.Messages)-1].Content, "hello from disk") {
			t.Errorf("tool result not in next model request: %+v", body.Messages)
		}
		var page fileReadPageForTest
		if err := json.Unmarshal([]byte(body.Messages[len(body.Messages)-1].Content), &page); err != nil || page.Content != "2: hello from disk" || page.StartLine != 2 || page.EndLine != 2 || page.NextLine != 3 {
			t.Errorf("model did not receive the requested source line: %+v, %v", page, err)
		}
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"I read the file.\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer srv.Close()
	command := workerproto.Command{RunID: "run-1", Prompt: "read that file", Model: modelcfg.Model{Provider: "openai-chat", ID: "test-model", BaseURL: srv.URL, APIKey: "test-key"}}
	var events []workerproto.Result
	answer, err := Run(context.Background(), command, func(event workerproto.Result) error { events = append(events, event); return nil })
	if err != nil || answer != "I read the file." {
		t.Fatalf("answer %q, error %v", answer, err)
	}
	var contextTokens []int64
	for _, event := range events {
		if event.Kind == "context_usage" {
			contextTokens = append(contextTokens, event.ContextUsage.Tokens)
		}
	}
	if len(contextTokens) != 4 || contextTokens[0] <= 0 || contextTokens[1] <= contextTokens[0] || contextTokens[2] <= contextTokens[1] || contextTokens[3] <= contextTokens[2] {
		t.Fatalf("context did not grow with tool calls, results and the final answer: %v", contextTokens)
	}
	var results []workerproto.ModelResult
	events, results = splitModelResults(events)
	if len(results) != 2 || results[0].FinishReason != "tool_calls" || results[1].FinishReason != "stop" {
		t.Fatalf("model turn results: %+v", results)
	}
	for _, result := range results {
		if result.DurationMS <= 0 {
			t.Fatalf("model request duration missing: %+v", result)
		}
	}
	if requests != 2 || len(events) != 6 || events[0].Kind != "model_request" || events[1].Status != "started" || events[2].Status != "succeeded" || events[3].Kind != "model_request" || events[4].Kind != "model_delta" || events[4].Content != answer || events[4].Turn != 2 || events[5].Kind != "model_complete" || events[5].FullBytes != len(answer) {
		t.Fatalf("unexpected execution: requests=%d events=%+v", requests, events)
	}
}

func TestLiveKeylessWebSearchThroughAgent(t *testing.T) {
	if os.Getenv("VISTA_LIVE_WEBSEARCH") != "1" {
		t.Skip("set VISTA_LIVE_WEBSEARCH=1 for live vendor checks")
	}
	for _, name := range []string{"exa", "tavily"} {
		t.Run(name, func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				var body struct {
					Messages []struct {
						Role    string `json:"role"`
						Content string `json:"content"`
					} `json:"messages"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				w.Header().Set("Content-Type", "text/event-stream")
				if requests == 1 {
					args, _ := json.Marshal(map[string]string{"provider": name, "query": "Go programming language"})
					chunk, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "search_1", "type": "function", "function": map[string]string{"name": "websearch", "arguments": string(args)}}}}, "finish_reason": "tool_calls"}}})
					fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", chunk)
					return
				}
				last := body.Messages[len(body.Messages)-1]
				var result struct {
					Provider string `json:"provider"`
					Results  []struct {
						URL string `json:"url"`
					} `json:"results"`
				}
				if last.Role != "tool" || json.Unmarshal([]byte(last.Content), &result) != nil || result.Provider != name || len(result.Results) == 0 || !strings.HasPrefix(result.Results[0].URL, "https://") {
					t.Errorf("live %s result was not handed to the Agent: %+v", name, last)
				}
				fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"Search complete.\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
			}))
			defer server.Close()
			command := workerproto.Command{
				RunID: "live-search", Prompt: "Search the web", Model: modelcfg.Model{Provider: "openai-chat", ID: "scripted", BaseURL: server.URL, APIKey: "fake"},
				SearchProviders: []workerproto.SearchProvider{{Name: name, Endpoint: map[string]string{"exa": "https://mcp.exa.ai/mcp", "tavily": "https://api.tavily.com/search"}[name]}},
			}
			answer, err := Run(context.Background(), command, func(workerproto.Result) error { return nil })
			if err != nil || answer != "Search complete." || requests != 2 {
				t.Fatalf("live %s Agent search: answer=%q requests=%d err=%v", name, answer, requests, err)
			}
		})
	}
}

func TestAgentExecutesFiveParallelModelTools(t *testing.T) {
	path := filepath.Join(t.TempDir(), "note.txt")
	if err := os.WriteFile(path, []byte("parallel contents"), 0600); err != nil {
		t.Fatal(err)
	}
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		var body struct {
			Messages []struct {
				Role       string `json:"role"`
				Content    string `json:"content"`
				ToolCallID string `json:"tool_call_id"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if requests == 1 {
			calls := make([]map[string]any, 5)
			for index := range calls {
				arguments, _ := json.Marshal(map[string]string{"path": path})
				calls[index] = map[string]any{"index": index, "id": fmt.Sprintf("call_%d", index), "type": "function", "function": map[string]string{"name": "read_file", "arguments": string(arguments)}}
			}
			event, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": calls}, "finish_reason": "tool_calls"}}})
			fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", event)
			return
		}
		seen := map[string]bool{}
		for _, message := range body.Messages {
			if message.Role == "tool" && strings.Contains(message.Content, "parallel contents") {
				seen[message.ToolCallID] = true
			}
		}
		if len(seen) != 5 {
			t.Errorf("five tool results missing from continuation: %+v", body.Messages)
		}
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"All five read."},"finish_reason":"stop"}]}`+"\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	command := workerproto.Command{RunID: "run-parallel", Prompt: "read five times", Model: modelcfg.Model{Provider: "openai-chat", ID: "mock", BaseURL: server.URL, APIKey: "key"}}
	var mu sync.Mutex
	var events []workerproto.Result
	answer, err := Run(context.Background(), command, func(event workerproto.Result) error {
		mu.Lock()
		events = append(events, event)
		mu.Unlock()
		return nil
	})
	if err != nil || answer != "All five read." || requests != 2 {
		t.Fatalf("parallel tools: answer=%q requests=%d err=%v", answer, requests, err)
	}
	finished := 0
	for _, event := range events {
		if event.Kind == "tool" && event.Status == "succeeded" {
			finished++
		}
	}
	if finished != 5 {
		t.Fatalf("parallel tools finished=%d, events=%+v", finished, events)
	}
}

func splitModelResults(events []workerproto.Result) ([]workerproto.Result, []workerproto.ModelResult) {
	other := make([]workerproto.Result, 0, len(events))
	results := []workerproto.ModelResult{}
	for _, event := range events {
		// Compaction and learning observations have their own assertions. Keep
		// these execution-order tests focused on model/tool lifecycle events.
		if event.Kind == "context_status" || event.Kind == "context_usage" || event.Kind == "model_observation" || event.Kind == "agent_task" {
			continue
		}
		if event.Kind == "model_result" && event.ModelResult != nil {
			results = append(results, *event.ModelResult)
		} else {
			other = append(other, event)
		}
	}
	return other, results
}

func TestAgentKeepsReasoningSeparateFromAnswer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"Check the inputs.\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"Answer 42.\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	command := workerproto.Command{RunID: "run-reasoning", Prompt: "solve", Model: modelcfg.Model{Provider: "openai-chat", ID: "mock", BaseURL: server.URL, APIKey: "key", ReasoningEffort: "high", ChatReasoningOverride: true}}
	var events []workerproto.Result
	answer, err := Run(context.Background(), command, func(event workerproto.Result) error { events = append(events, event); return nil })
	if err != nil || answer != "Answer 42." {
		t.Fatalf("answer=%q, err=%v", answer, err)
	}
	var reasoning, text string
	for _, event := range events {
		switch event.Kind {
		case "model_reasoning_delta":
			reasoning += event.Content
		case "model_delta":
			text += event.Content
		}
	}
	for _, event := range events {
		if event.Kind == "model_request" && event.ModelUse.ReasoningEffort != "high" {
			t.Fatalf("request lost actual effort: %+v", event)
		}
		if event.Kind == "model_result" && event.ModelResult.ReasoningEffort != "high" {
			t.Fatalf("result lost actual effort: %+v", event)
		}
	}
	if reasoning != "Check the inputs." || text != answer {
		t.Fatalf("reasoning=%q, text=%q, events=%+v", reasoning, text, events)
	}
}

func TestAgentReplaysSignedAnthropicThinkingForToolResult(t *testing.T) {
	path := filepath.Join(t.TempDir(), "note.txt")
	if err := os.WriteFile(path, []byte("signed tool result"), 0600); err != nil {
		t.Fatal(err)
	}
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests == 2 {
			var request struct {
				Messages []struct {
					Role    string          `json:"role"`
					Content json.RawMessage `json:"content"`
				} `json:"messages"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Error(err)
			}
			if len(request.Messages) != 3 || request.Messages[1].Role != "assistant" || !strings.Contains(string(request.Messages[1].Content), `"signature":"signed-tool-turn"`) || !strings.Contains(string(request.Messages[2].Content), "signed tool result") {
				t.Errorf("Anthropic signed tool history missing: %+v", request.Messages)
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if requests == 1 {
			fmt.Fprint(w, `data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking"}}`+"\n\n")
			fmt.Fprint(w, `data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"Find the file."}}`+"\n\n")
			fmt.Fprint(w, `data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"signed-tool-turn"}}`+"\n\n")
			fmt.Fprint(w, `data: {"type":"content_block_stop","index":0}`+"\n\n")
			fmt.Fprint(w, `data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"call-1","name":"read_file","input":{}}}`+"\n\n")
			args, _ := json.Marshal(map[string]string{"path": path})
			chunk, _ := json.Marshal(map[string]any{"type": "content_block_delta", "index": 1, "delta": map[string]any{"type": "input_json_delta", "partial_json": string(args)}})
			fmt.Fprintf(w, "data: %s\n\n", chunk)
			fmt.Fprint(w, `data: {"type":"content_block_stop","index":1}`+"\n\n")
			fmt.Fprint(w, `data: {"type":"message_delta","delta":{"stop_reason":"tool_use"}}`+"\n\n")
		} else {
			fmt.Fprint(w, `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Read it."}}`+"\n\n")
			fmt.Fprint(w, `data: {"type":"message_delta","delta":{"stop_reason":"end_turn"}}`+"\n\n")
		}
		fmt.Fprint(w, `data: {"type":"message_stop"}`+"\n\n")
	}))
	defer server.Close()
	command := workerproto.Command{RunID: "anthropic-thinking", Prompt: "read the file", Model: modelcfg.Model{Provider: "anthropic", ID: "mock", BaseURL: server.URL, APIKey: "key"}}
	var reasoning, text string
	answer, err := Run(context.Background(), command, func(event workerproto.Result) error {
		switch event.Kind {
		case "model_reasoning_delta":
			reasoning += event.Content
		case "model_delta":
			text += event.Content
		}
		return nil
	})
	if err != nil || requests != 2 || answer != "Read it." || text != answer || reasoning != "Find the file." {
		t.Fatalf("Anthropic Agent continuation: answer=%q text=%q reasoning=%q requests=%d err=%v", answer, text, reasoning, requests, err)
	}
}

func TestAgentReplaysMistralThinkingForToolResult(t *testing.T) {
	path := filepath.Join(t.TempDir(), "note.txt")
	if err := os.WriteFile(path, []byte("Mistral tool result"), 0600); err != nil {
		t.Fatal(err)
	}
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests == 2 {
			var request struct {
				Messages []struct {
					Role    string          `json:"role"`
					Content json.RawMessage `json:"content"`
				} `json:"messages"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Error(err)
			}
			if len(request.Messages) != 4 || request.Messages[2].Role != "assistant" || !strings.Contains(string(request.Messages[2].Content), `"type":"thinking"`) || !strings.Contains(string(request.Messages[2].Content), `"closed":true`) || !strings.Contains(string(request.Messages[3].Content), "Mistral tool result") {
				t.Errorf("Mistral structured tool history missing: %+v", request.Messages)
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if requests == 1 {
			args, _ := json.Marshal(map[string]string{"path": path})
			chunk, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{
				"content":    []any{map[string]any{"type": "thinking", "thinking": []any{map[string]string{"type": "text", "text": "Find the file."}}}},
				"tool_calls": []any{map[string]any{"index": 0, "id": "Abc123XYZ", "function": map[string]string{"name": "read_file", "arguments": string(args)}}},
			}, "finish_reason": "tool_calls"}}})
			fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", chunk)
			return
		}
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"Read it."},"finish_reason":"stop"}]}`+"\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	command := workerproto.Command{RunID: "mistral-thinking", Prompt: "read the file", Model: modelcfg.Model{Provider: "mistral", ID: "mock", BaseURL: server.URL, APIKey: "key"}}
	var reasoning string
	answer, err := Run(context.Background(), command, func(event workerproto.Result) error {
		if event.Kind == "model_reasoning_delta" {
			reasoning += event.Content
		}
		return nil
	})
	if err != nil || requests != 2 || answer != "Read it." || reasoning != "Find the file." {
		t.Fatalf("Mistral Agent continuation: answer=%q reasoning=%q requests=%d err=%v", answer, reasoning, requests, err)
	}
}

func TestAgentReplaysOpenRouterReasoningDetailsForToolResult(t *testing.T) {
	path := filepath.Join(t.TempDir(), "note.txt")
	if err := os.WriteFile(path, []byte("OpenRouter tool result"), 0600); err != nil {
		t.Fatal(err)
	}
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests == 2 {
			var request struct {
				Messages []struct {
					ReasoningDetails []map[string]any `json:"reasoning_details"`
					Reasoning        string           `json:"reasoning"`
				} `json:"messages"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Error(err)
			} else if len(request.Messages) != 4 || len(request.Messages[2].ReasoningDetails) != 1 || request.Messages[2].ReasoningDetails[0]["text"] != "Find the file." || request.Messages[2].Reasoning != "Find the file." {
				t.Errorf("OpenRouter reasoning details missing: %+v", request.Messages)
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if requests == 1 {
			args, _ := json.Marshal(map[string]string{"path": path})
			chunk, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{
				"reasoning_details": []any{map[string]any{"type": "reasoning.text", "id": "reason-1", "text": "Find the file."}},
				"tool_calls":        []any{map[string]any{"index": 0, "id": "call-1", "type": "function", "function": map[string]string{"name": "read_file", "arguments": string(args)}}},
			}, "finish_reason": "tool_calls"}}})
			fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", chunk)
			return
		}
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"Read it."},"finish_reason":"stop"}]}`+"\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	command := workerproto.Command{RunID: "openrouter-details", Prompt: "read the file", Model: modelcfg.Model{Provider: "openai-chat", ProviderID: "openrouter", ID: "mock", BaseURL: server.URL, APIKey: "key"}}
	var reasoning string
	answer, err := Run(context.Background(), command, func(event workerproto.Result) error {
		if event.Kind == "model_reasoning_delta" {
			reasoning += event.Content
		}
		return nil
	})
	if err != nil || requests != 2 || answer != "Read it." || reasoning != "Find the file." {
		t.Fatalf("OpenRouter Agent continuation: answer=%q reasoning=%q requests=%d err=%v", answer, reasoning, requests, err)
	}
}

func TestAgentReplaysMoonshotPlainReasoningForToolResult(t *testing.T) {
	path := filepath.Join(t.TempDir(), "note.txt")
	if err := os.WriteFile(path, []byte("Moonshot tool result"), 0600); err != nil {
		t.Fatal(err)
	}
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests == 2 {
			var request struct {
				Messages []struct {
					ReasoningContent string `json:"reasoning_content"`
					Content          string `json:"content"`
				} `json:"messages"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Error(err)
			} else if len(request.Messages) != 4 || request.Messages[2].ReasoningContent != "Find the file." || !strings.Contains(request.Messages[3].Content, "Moonshot tool result") {
				t.Errorf("Moonshot reasoning or tool result missing: %+v", request.Messages)
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if requests == 1 {
			args, _ := json.Marshal(map[string]string{"path": path})
			chunk, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{
				"reasoning_content": "Find the file.",
				"tool_calls":        []any{map[string]any{"index": 0, "id": "call-1", "type": "function", "function": map[string]string{"name": "read_file", "arguments": string(args)}}},
			}, "finish_reason": "tool_calls"}}})
			fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", chunk)
			return
		}
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"Read it."},"finish_reason":"stop"}]}`+"\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	command := workerproto.Command{RunID: "moonshot-reasoning", Prompt: "read the file", Model: modelcfg.Model{Provider: "openai-chat", ProviderID: "moonshotai", ID: "kimi-k2.7-code", BaseURL: server.URL, APIKey: "key"}}
	var reasoning string
	answer, err := Run(context.Background(), command, func(event workerproto.Result) error {
		if event.Kind == "model_reasoning_delta" {
			reasoning += event.Content
		}
		return nil
	})
	if err != nil || requests != 2 || answer != "Read it." || reasoning != "Find the file." {
		t.Fatalf("Moonshot Agent continuation: answer=%q reasoning=%q requests=%d err=%v", answer, reasoning, requests, err)
	}
}

func TestAgentReplaysObservedChatReasoningFieldForToolResult(t *testing.T) {
	path := filepath.Join(t.TempDir(), "note.txt")
	if err := os.WriteFile(path, []byte("field replay result"), 0600); err != nil {
		t.Fatal(err)
	}
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "text/event-stream")
		if requests == 1 {
			args, _ := json.Marshal(map[string]string{"path": path})
			chunk, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{
				"reasoning_text": "Find the file.",
				"tool_calls":     []any{map[string]any{"index": 0, "id": "call-1", "type": "function", "function": map[string]string{"name": "read_file", "arguments": string(args)}}},
			}, "finish_reason": "tool_calls"}}})
			fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", chunk)
			return
		}
		var request struct {
			Messages []struct {
				ReasoningText string `json:"reasoning_text"`
				Content       string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		} else if len(request.Messages) != 4 || request.Messages[2].ReasoningText != "Find the file." || !strings.Contains(request.Messages[3].Content, "field replay result") {
			t.Errorf("observed reasoning field or tool result missing: %+v", request.Messages)
		}
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"Read it."},"finish_reason":"stop"}]}`+"\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	command := workerproto.Command{RunID: "observed-chat-reasoning", Prompt: "read the file", Model: modelcfg.Model{Provider: "openai-chat", ProviderID: "custom-host", ID: "mock", BaseURL: server.URL, APIKey: "key"}}
	var reasoning string
	answer, err := Run(context.Background(), command, func(event workerproto.Result) error {
		if event.Kind == "model_reasoning_delta" {
			reasoning += event.Content
		}
		return nil
	})
	if err != nil || requests != 2 || answer != "Read it." || reasoning != "Find the file." {
		t.Fatalf("Chat reasoning field continuation: answer=%q reasoning=%q requests=%d err=%v", answer, reasoning, requests, err)
	}
}

func TestAgentReplaysOpenAIResponsesReasoningForToolResult(t *testing.T) {
	path := filepath.Join(t.TempDir(), "note.txt")
	if err := os.WriteFile(path, []byte("Responses tool result"), 0600); err != nil {
		t.Fatal(err)
	}
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests == 2 {
			var request struct {
				Input []map[string]any `json:"input"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Error(err)
			} else if len(request.Input) != 4 || request.Input[1]["type"] != "function_call" || request.Input[2]["type"] != "reasoning" || request.Input[2]["encrypted_content"] != "opaque-state" || request.Input[3]["type"] != "function_call_output" {
				t.Errorf("Responses Agent reasoning continuation missing: %+v", request.Input)
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if requests == 1 {
			args, _ := json.Marshal(map[string]string{"path": path})
			output, _ := json.Marshal(map[string]any{"type": "response.completed", "response": map[string]any{"status": "completed", "output": []any{
				map[string]any{"type": "function_call", "id": "fc_1", "call_id": "call_1", "name": "read_file", "arguments": string(args)},
				map[string]any{"type": "reasoning", "id": "rs_1", "summary": []any{map[string]string{"type": "summary_text", "text": "Find the file."}}, "encrypted_content": "opaque-state"},
			}}})
			fmt.Fprintf(w, "data: %s\n\n", output)
			return
		}
		fmt.Fprint(w, `data: {"type":"response.output_text.delta","item_id":"msg_2","delta":"Read it."}`+"\n\n")
		fmt.Fprint(w, `data: {"type":"response.completed","response":{"status":"completed","output":[]}}`+"\n\n")
	}))
	defer server.Close()
	command := workerproto.Command{RunID: "responses-reasoning", Prompt: "read the file", Model: modelcfg.Model{Provider: "openai-responses", ProviderID: "openai", ID: "mock", BaseURL: server.URL, APIKey: "key"}}
	answer, err := Run(context.Background(), command, func(workerproto.Result) error { return nil })
	if err != nil || requests != 2 || answer != "Read it." {
		t.Fatalf("Responses Agent continuation: answer=%q requests=%d err=%v", answer, requests, err)
	}
}

func TestAgentReportsProviderHostedToolWithoutExecutingIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "note.txt")
	if err := os.WriteFile(path, []byte("local file result"), 0600); err != nil {
		t.Fatal(err)
	}
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "text/event-stream")
		if requests == 1 {
			args, _ := json.Marshal(map[string]string{"path": path})
			output, _ := json.Marshal(map[string]any{"type": "response.completed", "response": map[string]any{"status": "completed", "output": []any{
				map[string]any{"type": "mcp_call", "id": "hosted_1", "status": "completed", "result": map[string]string{"name": "fixture"}},
				map[string]any{"type": "function_call", "id": "fc_1", "call_id": "local_1", "name": "read_file", "arguments": string(args)},
			}}})
			fmt.Fprintf(w, "data: %s\n\n", output)
			return
		}
		var request struct {
			Input []map[string]any `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		} else if len(request.Input) != 4 || request.Input[1]["type"] != "mcp_call" || request.Input[1]["id"] != "hosted_1" || request.Input[2]["type"] != "function_call" || request.Input[3]["type"] != "function_call_output" || !strings.Contains(fmt.Sprint(request.Input[3]["output"]), "local file result") {
			t.Errorf("hosted and local tool continuation order: %+v", request.Input)
		}
		fmt.Fprint(w, `data: {"type":"response.output_text.delta","item_id":"msg_2","delta":"Done."}`+"\n\n")
		fmt.Fprint(w, `data: {"type":"response.completed","response":{"status":"completed","output":[]}}`+"\n\n")
	}))
	defer server.Close()
	command := workerproto.Command{RunID: "hosted-tools", Prompt: "search and read", Model: modelcfg.Model{Provider: "openai-responses", ID: "mock", BaseURL: server.URL, APIKey: "key"}}
	var hosted, localStarted, localFinished int
	answer, err := Run(context.Background(), command, func(event workerproto.Result) error {
		if event.Kind == "tool" && event.Tool != nil {
			switch event.Tool.CallID {
			case "hosted_1":
				hosted++
				if event.Status != "succeeded" || event.Tool.Name != "mcp" || event.Tool.Summary != "Executed by Provider" || !event.Tool.ProviderExecuted {
					t.Errorf("wrong hosted tool event: %+v", event)
				}
			case "local_1":
				if event.Status == "started" {
					localStarted++
				} else if event.Status == "succeeded" {
					localFinished++
				}
			}
		}
		return nil
	})
	if err != nil || answer != "Done." || requests != 2 || hosted != 1 || localStarted != 1 || localFinished != 1 {
		t.Fatalf("hosted tool was lost or executed locally: answer=%q err=%v requests=%d hosted=%d local=%d/%d", answer, err, requests, hosted, localStarted, localFinished)
	}
}

func TestAgentContinuesAfterHostedOnlyResponsesTurn(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "text/event-stream")
		if requests == 1 {
			fmt.Fprint(w, `data: {"type":"response.completed","response":{"status":"completed","output":[{"type":"mcp_call","id":"hosted_1","status":"completed","result":{"name":"fixture"}}]}}`+"\n\n")
			return
		}
		var request struct {
			Input []map[string]any `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		} else if len(request.Input) != 2 || request.Input[1]["type"] != "mcp_call" || request.Input[1]["id"] != "hosted_1" {
			t.Errorf("hosted result was not replayed: %+v", request.Input)
		}
		fmt.Fprint(w, `data: {"type":"response.output_text.delta","item_id":"msg_2","delta":"Search complete."}`+"\n\n")
		fmt.Fprint(w, `data: {"type":"response.completed","response":{"status":"completed","output":[]}}`+"\n\n")
	}))
	defer server.Close()
	command := workerproto.Command{RunID: "hosted-only", Prompt: "search", Model: modelcfg.Model{Provider: "openai-responses", ID: "mock", BaseURL: server.URL, APIKey: "key"}}
	hosted := 0
	answer, err := Run(context.Background(), command, func(event workerproto.Result) error {
		if event.Kind == "tool" {
			hosted++
			if event.Tool == nil || !event.Tool.ProviderExecuted || event.Status != "succeeded" {
				t.Errorf("unexpected hosted event: %+v", event)
			}
		}
		return nil
	})
	if err != nil || answer != "Search complete." || hosted != 1 || requests != 2 {
		t.Fatalf("hosted-only continuation: answer=%q hosted=%d requests=%d err=%v", answer, hosted, requests, err)
	}
}

func TestAgentReportsPerplexitySources(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"citations":["https://example.com/source"],"choices":[{"delta":{"content":"Cited answer"},"finish_reason":"stop"}]}`+"\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	noTools := false
	command := workerproto.Command{RunID: "perplexity-sources", Prompt: "question", Model: modelcfg.Model{Provider: "openai-chat", ProviderID: "perplexity", ID: "sonar-pro", BaseURL: server.URL, APIKey: "key", Tools: &noTools}}
	var sources []workerproto.Result
	answer, err := Run(context.Background(), command, func(event workerproto.Result) error {
		if event.Kind == "source" {
			sources = append(sources, event)
		}
		return nil
	})
	if err != nil || answer != "Cited answer" || requests != 1 || len(sources) != 1 || sources[0].Content != "https://example.com/source" || sources[0].Turn != 1 {
		t.Fatalf("Perplexity Agent sources: answer=%q requests=%d sources=%+v err=%v", answer, requests, sources, err)
	}
}

func TestAgentReportsOpenRouterWebCitations(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"Cited answer","annotations":[{"type":"url_citation","url_citation":{"url":"https://example.com/source"}}]},"finish_reason":"stop"}]}`+"\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	noTools := false
	command := workerproto.Command{RunID: "openrouter-sources", Prompt: "question", Model: modelcfg.Model{Provider: "openai-chat", ProviderID: "openrouter", ID: "mock", BaseURL: server.URL, APIKey: "key", Tools: &noTools}}
	var sources []workerproto.Result
	answer, err := Run(context.Background(), command, func(event workerproto.Result) error {
		if event.Kind == "source" {
			sources = append(sources, event)
		}
		return nil
	})
	if err != nil || answer != "Cited answer" || len(sources) != 1 || sources[0].Content != "https://example.com/source" || sources[0].Turn != 1 {
		t.Fatalf("OpenRouter Agent sources: answer=%q sources=%+v err=%v", answer, sources, err)
	}
}

func TestToollessLocalModelReceivesNoToolSchemas(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("unexpected local model route: %s", r.URL.Path)
		}
		var request struct {
			Tools []any `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if len(request.Tools) != 0 {
			t.Errorf("tool-less model was offered %d tools", len(request.Tools))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"local answer\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	for _, provider := range []string{"ollama", "vllm"} {
		t.Run(provider, func(t *testing.T) {
			tools := false
			command := workerproto.Command{RunID: "local-run", Prompt: "hello", Model: modelcfg.Model{Provider: "openai-chat", ProviderID: provider, ID: "local-model", BaseURL: server.URL + "/v1", APIKey: "-", Tools: &tools}}
			var used string
			answer, err := Run(context.Background(), command, func(result workerproto.Result) error {
				if result.ModelUse != nil {
					used = result.ModelUse.Provider
				}
				return nil
			})
			if err != nil || answer != "local answer" || used != provider {
				t.Fatalf("local model result: %q, %q, %v", answer, used, err)
			}
		})
	}
}
