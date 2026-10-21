package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	modelcfg "vista/internal/model"
	"vista/internal/provider"
	"vista/internal/resources"
	"vista/internal/workerproto"
)

func TestCompactorPreservesProtectedMessagesAndReplacesItsSummary(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var summaries atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		version := summaries.Add(1)
		compactionResponse(w, []any{compactionText(fmt.Sprintf("summary version %d", version))})
	}))
	defer server.Close()
	command := workerproto.Command{RunID: "protected", Prompt: "original user", Model: modelcfg.Model{
		Provider: "openai-responses", ID: "test", BaseURL: server.URL + "/v1", APIKey: "test-key", Context: 12000,
	}}
	protected := []provider.ChatMessage{
		{Role: "system", Content: "original system"},
		{Role: "system", Content: "second system instruction"},
		{Role: "user", Content: "original user", Resources: []resources.Ref{putResourceFixture(t, []byte("\x89PNG\r\n\x1a\npreserve original image bytes"), "image/png")}},
		{Role: "user", Content: "interjection"},
	}
	messages := append([]provider.ChatMessage(nil), protected...)
	compactor := executionCompactor{summaryIndex: -1}
	scope := &runScope{}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for pass := 1; pass <= 2; pass++ {
		for id := 0; id < 20; id++ {
			call := provider.ToolCall{ID: fmt.Sprintf("old-%d-%d", pass, id), Type: "function"}
			call.Function.Name, call.Function.Arguments = "read_file", `{}`
			messages = append(messages, provider.ChatMessage{Role: "assistant", ToolCalls: []provider.ToolCall{call}}, provider.ChatMessage{Role: "tool", ToolCallID: call.ID, Content: strings.Repeat("x", 1800)})
		}
		last := opaqueCompactionGroup(fmt.Sprintf("latest-%d", pass))
		messages = append(messages, last...)
		compacted, changed, err := compactor.compact(ctx, command, messages, nil, func(workerproto.Result) error { return nil }, scope, 0)
		if err != nil || !changed || len(compacted) >= len(messages) {
			t.Fatalf("pass %d changed=%v before=%d after=%d err=%v", pass, changed, len(messages), len(compacted), err)
		}
		var actualProtected []provider.ChatMessage
		for _, message := range compacted {
			if message.Role == "system" || message.Role == "user" {
				actualProtected = append(actualProtected, message)
			}
		}
		if !reflect.DeepEqual(actualProtected, protected) {
			t.Fatal("system or user messages, including images, changed during compaction")
		}
		if !reflect.DeepEqual(compacted[len(compacted)-len(last):], last) {
			t.Fatal("latest complete assistant/tool group lost original opaque protocol state")
		}
		text := ""
		for _, message := range compacted {
			text += message.Content
		}
		current := summaries.Load()
		if strings.Count(text, fmt.Sprintf("summary version %d", current)) != 1 {
			t.Fatalf("summary accumulated or nested at pass %d", pass)
		}
		for version := int32(1); version < current; version++ {
			if slices.ContainsFunc(compacted, func(m provider.ChatMessage) bool {
				return m.Content == executionSummaryPrefix+fmt.Sprintf("summary version %d", version)
			}) {
				t.Fatalf("old summary version %d survived pass %d", version, pass)
			}
		}
		messages = compacted
	}
	if summaries.Load() < 2 {
		t.Fatalf("expected repeated compaction, got %d summary requests", summaries.Load())
	}
}

func opaqueCompactionGroup(id string) []provider.ChatMessage {
	call := provider.ToolCall{ID: id, Type: "function", ThoughtSignature: "gemini-signature", GeminiID: "gemini-call"}
	call.Function.Name, call.Function.Arguments = "read_file", `{}`
	wireCall, _ := json.Marshal(map[string]string{"type": "function_call", "id": "fc", "call_id": id, "name": "read_file", "arguments": `{}`})
	assistant := provider.ChatMessage{
		Role: "assistant", Content: "latest work", ToolCalls: []provider.ToolCall{call},
		AnthropicBlocks:  []json.RawMessage{json.RawMessage(`{"type":"thinking","thinking":"thought","signature":"anthropic-signature"}`)},
		BedrockParts:     []provider.BedrockPart{{Kind: "reasoning", Text: "thought", Signature: "bedrock-signature", Redacted: []byte("opaque-bytes")}},
		MistralParts:     []provider.MistralPart{{Type: "thinking", Text: "mistral-thought"}},
		ReasoningDetails: []json.RawMessage{json.RawMessage(`{"type":"reasoning.encrypted","data":"chat-signature"}`)},
		ReasoningText:    "chat-thought", ReasoningField: "reasoning_content",
		ResponseReasoning: []json.RawMessage{json.RawMessage(`{"type":"reasoning","id":"rs","summary":[],"encrypted_content":"response-signature"}`)},
		ResponseOutput:    []json.RawMessage{json.RawMessage(`{"type":"reasoning","id":"rs","summary":[],"encrypted_content":"response-signature"}`), wireCall},
	}
	return []provider.ChatMessage{assistant, {Role: "tool", ToolCallID: id, Content: `{"content":"latest output"}`}}
}

func TestUnknownWindowCompactsOnlyStructuredContextErrors(t *testing.T) {
	for _, test := range []struct {
		name, code, message string
		status              int
		repeat              bool
		wantRetry           bool
	}{
		{"context overflow", "context_length_exceeded", "input is too long for requested model", 400, false, true},
		{"ordinary HTTP error", "invalid_request_error", "Invalid parameter", 400, false, false},
		{"context text without code", "", "prompt is too long", 400, false, true},
		{"irreducible repeated overflow", "context_length_exceeded", "context_length_exceeded", 400, true, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("XDG_CONFIG_HOME", root)
			written := filepath.Join(root, "once.txt")
			var requests, summaries atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					Instructions string `json:"instructions"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				if strings.HasPrefix(body.Instructions, "Compress the completed execution transcript") {
					summaries.Add(1)
					compactionResponse(w, []any{compactionText("saved finding")})
					return
				}
				turn := requests.Add(1)
				if turn <= 4 {
					arguments, _ := json.Marshal(map[string]string{"path": written, "expectedSha256": "missing", "content": "written once"})
					name := "write_file"
					if turn > 1 {
						name = "read_file"
						arguments, _ = json.Marshal(map[string]string{"path": written})
					}
					compactionResponse(w, []any{map[string]any{"type": "function_call", "id": fmt.Sprintf("fc_%d", turn), "call_id": fmt.Sprintf("call_%d", turn), "name": name, "arguments": string(arguments)}})
					return
				}
				if turn == 5 || test.repeat {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(test.status)
					_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": test.code, "message": test.message}})
					return
				}
				compactionResponse(w, []any{compactionText("Completed after compression.")})
			}))
			defer server.Close()
			command := workerproto.Command{RunID: "unknown-window", Prompt: "Write once, inspect, then finish.", Model: modelcfg.Model{Provider: "openai-responses", ID: "unknown", BaseURL: server.URL + "/v1", APIKey: "test-key"}}
			started, succeeded := 0, 0
			var compacting, compacted *workerproto.CompactionStatus
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			answer, err := Run(ctx, command, func(event workerproto.Result) error {
				if event.Context != nil {
					snapshot := *event.Context
					switch snapshot.Phase {
					case "compacting":
						compacting = &snapshot
					case "compacted":
						compacted = &snapshot
					}
				}
				if event.Kind == "tool" {
					if event.Status == "started" {
						started++
					} else if event.Status == "succeeded" {
						succeeded++
					} else {
						return fmt.Errorf("completed side effect was replayed or failed")
					}
				}
				return nil
			})
			if test.wantRetry && !test.repeat {
				if err != nil || answer != "Completed after compression." || requests.Load() != 6 || summaries.Load() < 1 {
					t.Fatalf("context retry answer=%q requests=%d summaries=%d err=%v", answer, requests.Load(), summaries.Load(), err)
				}
				if compacting == nil || compacted == nil {
					t.Fatalf("missing compaction statuses: %+v %+v", compacting, compacted)
				}

			} else {
				var httpError *provider.ModelHTTPError
				if !errors.As(err, &httpError) || httpError.StatusCode != test.status || errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("did not return the provider failure promptly: %v", err)
				}
				if !test.wantRetry && (requests.Load() != 5 || summaries.Load() != 0) || test.repeat && (requests.Load() <= 5 || requests.Load() > 7 || summaries.Load() < 1 || summaries.Load() > 16) {
					t.Fatalf("unexpected retry loop: requests=%d summaries=%d", requests.Load(), summaries.Load())
				}
			}
			if started != 4 || succeeded != 4 {
				t.Fatalf("tools were replayed: started=%d succeeded=%d", started, succeeded)
			}
			if data, err := os.ReadFile(written); err != nil || string(data) != "written once" {
				t.Fatalf("side effect changed: %q %v", data, err)
			}
		})
	}
}

func TestCompactorRejectsIrreducibleContextWithoutChangingIt(t *testing.T) {
	command := workerproto.Command{RunID: "no-groups"}
	messages := []provider.ChatMessage{{Role: "system", Content: "instructions"}, {Role: "user", Content: "task"}}
	compactor := newExecutionCompactor(messages)
	next, changed, err := compactor.compact(context.Background(), command, messages, nil, func(workerproto.Result) error { return nil }, &runScope{}, 0)
	if err == nil || changed || !reflect.DeepEqual(messages, next) {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
}
