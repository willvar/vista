//go:build linux

package worker

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
	"vista/internal/store"
	"vista/internal/workerproto"
)

func TestWorkerRestartRestoresToolEvidenceWithoutExecutingItAgain(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	state := filepath.Join(root, "vista", "state")
	if err := os.MkdirAll(state, 0700); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(filepath.Join(state, "vista.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Create(ctx, "session", "worker restart"); err != nil {
		t.Fatal(err)
	}
	const evidence = "TOOL_EVIDENCE_SURVIVES_WORKER_RESTART"
	path := filepath.Join(root, "source.txt")
	if err := os.WriteFile(path, []byte(evidence), 0600); err != nil {
		t.Fatal(err)
	}
	var requests, followups atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Messages []provider.ChatMessage }
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if requests.Add(1) == 1 {
			args, _ := json.Marshal(map[string]string{"path": path})
			chunk, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "saved-read", "type": "function", "function": map[string]string{"name": "read_file", "arguments": string(args)}}}}, "finish_reason": "tool_calls"}}})
			fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", chunk)
			return
		}
		if body.Messages[len(body.Messages)-1].Role == "user" {
			calls, results, final := 0, 0, 0
			for _, m := range body.Messages {
				calls += len(m.ToolCalls)
				if m.Role == "tool" && m.ToolCallID == "saved-read" && strings.Contains(m.Content, evidence) {
					results++
				}
				if m.Role == "assistant" && m.Content == "Checked." {
					final++
				}
			}
			if calls != 1 || results != 1 || final != 1 {
				t.Errorf("restored request: calls=%d results=%d final=%d", calls, results, final)
			}
			followups.Add(1)
		}
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"Checked.\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer upstream.Close()
	model := modelcfg.Model{Provider: "openai-chat", ID: "fixture", BaseURL: upstream.URL, APIKey: "test"}
	runWorker := func(runID, prompt string) {
		t.Helper()
		user, _, err := db.BeginRun(ctx, "session", runID, runID+"-user", prompt)
		if err != nil {
			t.Fatal(err)
		}
		input, results, stop := startWorkerFixtureProcess(t, root)
		defer stop() // Each run uses a separate OS process with no shared Agent memory.
		if err := input.Encode(workerproto.Command{SessionID: "session", RunID: runID, MessageSequence: user.Sequence, Prompt: prompt, Model: model}); err != nil {
			t.Fatal(err)
		}
		deadline := time.After(10 * time.Second)
		for {
			select {
			case result, ok := <-results:
				if !ok {
					t.Fatal("Worker exited before run completion")
				}
				var err error
				switch result.Kind {
				case "model_history":
					_, err = db.RecordModelHistory(ctx, "session", result)
				case "model_request":
					_, err = db.RecordModelRequestEvent(ctx, "session", result)
				case "model_delta":
					_, err = db.AppendModelDelta(ctx, "session", runID, result.Turn, result.Depth, result.Content)
				case "tool":
					_, err = db.AppendRunEvent(ctx, "session", runID, result.Kind, result)
				case "":
					if result.Status != "succeeded" {
						t.Fatalf("Worker failed: %+v", result)
					}
					_, err = db.FinishRun(ctx, "session", runID, runID+"-answer", result.Status, result.Content, store.ModelOutputRef{Turn: result.Turn, Bytes: result.FullBytes})
					if err != nil {
						t.Fatal(err)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				if result.AckID != "" {
					if err := input.Encode(workerproto.Command{Action: "ack", RunID: runID, AckID: result.AckID}); err != nil {
						t.Fatal(err)
					}
				}
			case <-deadline:
				t.Fatal("Worker did not complete")
			}
		}
	}
	runWorker("first", "Read the source.")
	// The evidence can now only come from history, not another successful read.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	runWorker("next", "Where did you get the evidence?")
	if followups.Load() != 1 || requests.Load() != 3 {
		t.Fatalf("unexpected requests=%d verified follow-ups=%d", requests.Load(), followups.Load())
	}
}
