package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	modelcfg "vista/internal/model"
	"vista/internal/provider"
	"vista/internal/store"
	"vista/internal/workerproto"
)

func TestAgentOffersChecklistAlongsideMCPTools(t *testing.T) {
	modelHTTP := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Tools []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		names := make(map[string]bool, len(request.Tools))
		for _, tool := range request.Tools {
			names[tool.Function.Name] = true
		}
		for _, name := range []string{"update_checklist", "mcp_list_tools", "mcp_call_tool"} {
			if !names[name] {
				t.Errorf("model request is missing %s: %v", name, names)
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"done\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer modelHTTP.Close()
	command := workerproto.Command{RunID: "run", Prompt: "plan with MCP", Model: modelcfg.Model{Provider: "openai-chat", ID: "test", BaseURL: modelHTTP.URL, APIKey: "key"}, MCPServers: []workerproto.MCPServer{{ID: "remote", Name: "Remote tool service", Transport: "http", Endpoint: "http://127.0.0.1:1"}}}
	answer, err := Run(context.Background(), command, func(workerproto.Result) error { return nil })
	if err != nil || answer != "done" {
		t.Fatalf("answer=%q err=%v", answer, err)
	}
}

func TestAgentDiscoversAndCallsRemoteMCPTool(t *testing.T) {
	mcpServer := mcp.NewServer(&mcp.Implementation{Name: "fixture", Version: "1"}, nil)
	mcpServer.AddTool(&mcp.Tool{Name: "echo", Description: "Echo text", InputSchema: json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}}}`)}, func(_ context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal(request.Params.Arguments, &args); err != nil {
			return nil, err
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: args.Text}}}, nil
	})
	mcpHTTP := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return mcpServer }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true}))
	defer mcpHTTP.Close()
	modelCalls := 0
	modelHTTP := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		modelCalls++
		var request struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		var event any
		switch modelCalls {
		case 1:
			event = mcpToolEvent("list_1", "mcp_list_tools", `{"server":"remote"}`)
		case 2:
			if !strings.Contains(request.Messages[len(request.Messages)-1].Content, "echo") {
				t.Error("MCP discovery was not returned to model")
			}
			event = mcpToolEvent("call_1", "mcp_call_tool", `{"server":"remote","name":"echo","arguments":{"text":"hello"}}`)
		default:
			if !strings.Contains(request.Messages[len(request.Messages)-1].Content, "hello") {
				t.Error("MCP call result was not returned to model")
			}
			event = map[string]any{"choices": []any{map[string]any{"delta": map[string]string{"content": "MCP said hello"}, "finish_reason": "stop"}}}
		}
		encoded, _ := json.Marshal(event)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", encoded)
	}))
	defer modelHTTP.Close()
	command := workerproto.Command{RunID: "run", Prompt: "ask MCP", Model: modelcfg.Model{Provider: "openai-chat", ID: "test", BaseURL: modelHTTP.URL, APIKey: "key"}, MCPServers: []workerproto.MCPServer{{ID: "remote", Name: "Remote tool service", Transport: "http", Endpoint: mcpHTTP.URL}}}
	var events []workerproto.Result
	answer, err := Run(context.Background(), command, func(event workerproto.Result) error { events = append(events, event); return nil })
	var results []workerproto.ModelResult
	events, results = splitModelResults(events)
	if len(results) != 3 {
		t.Fatalf("MCP model turn results: %+v", results)
	}
	if err != nil || answer != "MCP said hello" || modelCalls != 3 || len(events) != 9 || events[7].Kind != "model_delta" || events[7].Content != answer || events[8].Kind != "model_complete" {
		t.Fatalf("MCP agent flow: answer=%q err=%v modelCalls=%d events=%+v", answer, err, modelCalls, events)
	}
}

func mcpToolEvent(id, name, arguments string) any {
	return map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": id, "type": "function", "function": map[string]string{"name": name, "arguments": arguments}}}}, "finish_reason": "tool_calls"}}}
}

func TestAgentDoesNotReplayMCPCallAfterConnectionBreak(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "disconnect-fixture", Version: "1"}, nil)
	server.AddTool(&mcp.Tool{Name: "effect", InputSchema: json.RawMessage(`{"type":"object"}`)}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "done"}}}, nil
	})
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	var calls atomic.Int32
	mcpHTTP := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		originalBody := r.Body
		body, err := io.ReadAll(io.LimitReader(originalBody, 4097))
		_ = originalBody.Close()
		if err != nil {
			t.Error(err)
			return
		}
		if len(body) > 4096 {
			http.Error(w, "MCP test request too large", http.StatusRequestEntityTooLarge)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		if bytes.Contains(body, []byte(`"method":"tools/call"`)) {
			calls.Add(1)
			connection, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			_ = connection.Close()
			return
		}
		handler.ServeHTTP(w, r)
	}))
	defer mcpHTTP.Close()
	var modelCalls atomic.Int32
	modelHTTP := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := modelCalls.Add(1)
		var request struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		var event any
		switch call {
		case 1:
			event = mcpToolEvent("effect_1", "mcp_call_tool", `{"server":"remote","name":"effect","arguments":{}}`)
		case 2:
			last := request.Messages[len(request.Messages)-1]
			if last.Role != "tool" || !strings.Contains(last.Content, "error") {
				t.Errorf("MCP connection error was not returned to the model: %+v", last)
			}
			event = map[string]any{"choices": []any{map[string]any{"delta": map[string]string{"content": "MCP outcome unknown"}, "finish_reason": "stop"}}}
		default:
			t.Errorf("unexpected model replay turn %d", call)
			return
		}
		encoded, _ := json.Marshal(event)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", encoded)
	}))
	defer modelHTTP.Close()
	command := workerproto.Command{RunID: "run", Prompt: "call effect", Model: modelcfg.Model{Provider: "openai-chat", ID: "test", BaseURL: modelHTTP.URL, APIKey: "key"}, MCPServers: []workerproto.MCPServer{{ID: "remote", Name: "Remote tool service", Transport: "http", Endpoint: mcpHTTP.URL}}}
	var events []workerproto.Result
	answer, err := Run(context.Background(), command, func(event workerproto.Result) error { events = append(events, event); return nil })
	if err != nil || answer != "MCP outcome unknown" || calls.Load() != 1 || modelCalls.Load() != 2 {
		t.Fatalf("disconnected MCP call was replayed or hidden: answer=%q err=%v calls=%d modelCalls=%d", answer, err, calls.Load(), modelCalls.Load())
	}
	foundFailure := false
	for _, event := range events {
		if event.Kind == "tool" && event.Status == "failed" && event.Tool != nil && event.Tool.Name == "mcp_call_tool" {
			foundFailure = true
		}
	}
	if !foundFailure {
		t.Fatalf("disconnected MCP call was not recorded as failed: %+v", events)
	}
}

func TestAgentPreservesLargerMCPResultAsArtifact(t *testing.T) {
	for _, failure := range []bool{false, true} {
		t.Run(fmt.Sprintf("failure=%v", failure), func(t *testing.T) {
			db, ctx := checkpointFixture(t)
			const contentSize = 60 * 1024
			mcpServer := mcp.NewServer(&mcp.Implementation{Name: "large-result", Version: "1"}, nil)
			mcpServer.AddTool(&mcp.Tool{Name: "large", InputSchema: json.RawMessage(`{"type":"object"}`)}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				return &mcp.CallToolResult{IsError: failure, StructuredContent: map[string]any{"diagnostic": "STRUCTURED_DETAILS"}, Content: []mcp.Content{&mcp.TextContent{Text: strings.Repeat("x", contentSize) + "OUTPUT_END"}}}, nil
			})
			mcpHTTP := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return mcpServer }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true}))
			defer mcpHTTP.Close()
			modelCalls := 0
			modelHTTP := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				modelCalls++
				var request struct {
					Messages []struct {
						Role    string `json:"role"`
						Content string `json:"content"`
					} `json:"messages"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
					return
				}
				var event any
				if modelCalls == 1 {
					event = mcpToolEvent("large_1", "mcp_call_tool", `{"server":"remote","name":"large","arguments":{}}`)
				} else {
					var result struct {
						ArtifactID string `json:"artifactId"`
						OutputPath string `json:"outputPath"`
						Bytes      int    `json:"bytes"`
						Complete   bool   `json:"complete"`
						Error      string `json:"error"`
					}
					last := request.Messages[len(request.Messages)-1]
					if last.Role != "tool" || json.Unmarshal([]byte(last.Content), &result) != nil || result.ArtifactID == "" || !result.Complete || result.Bytes <= contentSize {
						t.Errorf("large MCP result was not preserved as an artifact: %+v, %s", result, last.Content)
					}
					if (result.Error != "") != failure {
						t.Errorf("MCP failure status lost: %s", last.Content)
					}
					if result.OutputPath != "" || strings.Contains(last.Content, "outputPath") || !strings.Contains(last.Content, "read_tool_output") {
						t.Errorf("MCP output should be recoverable by artifact ID without a local path: %s", last.Content)
					}
					event = map[string]any{"choices": []any{map[string]any{"delta": map[string]string{"content": "MCP output saved"}, "finish_reason": "stop"}}}
				}
				encoded, _ := json.Marshal(event)
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", encoded)
			}))
			defer modelHTTP.Close()
			user, _, err := db.BeginRun(ctx, "session", "run", "u1", "call large MCP")
			if err != nil {
				t.Fatal(err)
			}
			command := workerproto.Command{SessionID: "session", MessageSequence: user.Sequence, RunID: "run", Prompt: "call large MCP", Model: modelcfg.Model{Provider: "openai-chat", ID: "test", BaseURL: modelHTTP.URL, APIKey: "key"}, MCPServers: []workerproto.MCPServer{{ID: "remote", Name: "Remote tool service", Transport: "http", Endpoint: mcpHTTP.URL}}}
			var events []workerproto.Result
			var final store.ModelOutputRef
			sink := historyTestSink(db, command, &final)
			answer, err := Run(ctx, command, func(event workerproto.Result) error { events = append(events, event); return sink(event) })
			if err != nil || answer != "MCP output saved" || modelCalls != 2 {
				t.Fatalf("large MCP Agent flow: answer=%q, modelCalls=%d, err=%v", answer, modelCalls, err)
			}
			started, completed, resultRecorded := false, false, false
			expectedStatus := "succeeded"
			if failure {
				expectedStatus = "failed"
			}
			for _, event := range events {
				if event.Kind == "artifact" && event.Status == "started" {
					started = true
				}
				if event.Kind == "artifact" && event.Status == "succeeded" && event.Artifact != nil && event.Artifact.Complete {
					completed = true
					data, readErr := os.ReadFile(event.Artifact.OutputPath)
					if readErr != nil || !strings.Contains(string(data), strings.Repeat("x", contentSize)+"OUTPUT_END") || !strings.Contains(string(data), "STRUCTURED_DETAILS") {
						t.Errorf("full MCP artifact missing: bytes=%d, err=%v", len(data), readErr)
					}
				}
				if event.Kind == "tool" && event.Status == expectedStatus && event.Tool != nil && event.Tool.Name == "mcp_call_tool" {
					resultRecorded = true
				}
			}
			if !started || !completed || !resultRecorded {
				t.Fatalf("large MCP output lost its tool status or artifact: %+v", events)
			}

			if _, err := db.FinishRun(ctx, "session", "run", "a1", "succeeded", answer, final); err != nil {
				t.Fatal(err)
			}
			user, _, err = db.BeginRun(ctx, "session", "following", "u2", "Inspect the saved output.")
			if err != nil {
				t.Fatal(err)
			}
			command.RunID, command.MessageSequence = "following", user.Sequence
			loaded, err := loadConversationContext(ctx, command, 0)
			if err != nil {
				t.Fatal(err)
			}
			var result *provider.ChatMessage
			for i := range loaded {
				if loaded[i].Role == "tool" && loaded[i].ToolCallID == "large_1" {
					result = &loaded[i]
				}
			}
			if result == nil || !strings.Contains(result.Content, "artifactId") || (strings.Contains(result.Content, `"error"`)) != failure {
				t.Fatalf("restored MCP result lost its artifact or failure: %+v", result)
			}
		})
	}
}
