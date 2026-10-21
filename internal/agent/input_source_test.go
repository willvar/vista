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
	"sync/atomic"
	"testing"
	"time"

	modelcfg "vista/internal/model"
	"vista/internal/provider"
	"vista/internal/workerproto"
)

func TestInterjectionRetainsCompletePreviousAnswer(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	answer := strings.Repeat("x", 10000) + "ANSWER_TAIL_EVIDENCE"
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []provider.ChatMessage `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		text := answer
		if calls.Add(1) > 1 {
			text = "Final reply."
			var previous string
			for _, message := range request.Messages {
				if message.Role == "assistant" {
					previous = message.Content
				}
			}
			if previous != answer {
				t.Errorf("previous answer truncated: got %d bytes, want %d", len(previous), len(answer))
			}
		}
		payload, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]string{"content": text}, "finish_reason": "stop"}}})
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", payload)
	}))
	defer upstream.Close()
	command := workerproto.Command{RunID: "run", Prompt: "First request.", Model: modelcfg.Model{Provider: "openai-chat", ID: "fixture", APIKey: "test", BaseURL: upstream.URL}}
	delivered := false
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	result, err := RunWithInputSource(ctx, command, func(workerproto.Result) error { return nil }, func(reply *workerproto.CompletedReply) (*workerproto.Command, error) {
		if reply == nil || delivered {
			return nil, nil
		}
		delivered = true
		if reply.FullBytes != len(answer) || reply.Content == answer {
			t.Errorf("long answer checkpoint must retain a bounded display preview: %+v", reply)
		}
		return &workerproto.Command{RunID: command.RunID, InputID: "followup", Prompt: "Follow up on the previous answer."}, nil
	}, nil)
	if err != nil || result != "Final reply." || calls.Load() != 2 || !delivered {
		t.Fatalf("interjected answer=%q err=%v requests=%d delivered=%v", result, err, calls.Load(), delivered)
	}
}

func TestDurableInputSourceAcceptsInterjectionAfterCompressionBeforeNextRequest(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	path := filepath.Join(root, "input.txt")
	if err := os.WriteFile(path, []byte(strings.Repeat("x", 1536)), 0600); err != nil {
		t.Fatal(err)
	}
	const text = "Constraint arriving during compression"
	var calls, summaries atomic.Int32
	var pending, delivered atomic.Bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model        string           `json:"model"`
			Instructions string           `json:"instructions"`
			Input        []map[string]any `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		if strings.HasPrefix(body.Instructions, "Compress the completed execution transcript") {
			if summaries.Add(1) == 1 {
				pending.Store(true)
			}
			compactionResponse(w, []any{compactionText("Completed reads remain recorded.")})
			return
		}
		if body.Model != "bound-model" {
			t.Errorf("interjection changed bound model: %s", body.Model)
		}
		if summaries.Load() > 0 {
			found := false
			for _, item := range body.Input {
				if item["role"] == "user" && item["content"] == text {
					found = true
				}
			}
			if !found {
				t.Error("resumed ordinary request omitted input received during compression")
			}
		}
		turn := calls.Add(1)
		if turn == 8 {
			w.WriteHeader(400)
			fmt.Fprint(w, `{"error":{"message":"prompt is too long"}}`)
			return
		}
		if turn > 12 {
			compactionResponse(w, []any{compactionText("done")})
			return
		}
		arguments, _ := json.Marshal(map[string]string{"path": path})
		compactionResponse(w, []any{map[string]any{"type": "function_call", "id": fmt.Sprintf("fc_%d", turn), "call_id": fmt.Sprintf("call_%d", turn), "name": "read_file", "arguments": string(arguments)}})
	}))
	defer upstream.Close()
	command := workerproto.Command{RunID: "source-run", Prompt: "Read and retain findings.", Model: modelcfg.Model{Provider: "openai-responses", ID: "bound-model", BaseURL: upstream.URL + "/v1", APIKey: "key", Context: 8192}}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	answer, err := RunWithInputSource(ctx, command, func(workerproto.Result) error { return nil }, func(*workerproto.CompletedReply) (*workerproto.Command, error) {
		if pending.Swap(false) {
			delivered.Store(true)
			return &workerproto.Command{RunID: command.RunID, InputID: "during-compression", Prompt: text, Model: modelcfg.Model{Provider: "anthropic", ID: "wrong-model"}}, nil
		}
		return nil, nil
	}, nil)
	if err != nil || answer != "done" || summaries.Load() == 0 || !delivered.Load() {
		t.Fatalf("answer=%q err=%v summaries=%d delivered=%v", answer, err, summaries.Load(), delivered.Load())
	}
}
