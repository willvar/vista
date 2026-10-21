//go:build linux

package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	modelcfg "vista/internal/model"
	"vista/internal/workerproto"
)

func TestTimedOutShellRetainsPartialOutputForModelAndRecovery(t *testing.T) {
	_, record := outputToolStore(t)
	root := t.TempDir()
	requests := 0
	var artifactID string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		var request struct {
			Messages []struct{ Role, Content string }
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		var delta any
		finishReason := "stop"
		if requests == 1 {
			args, _ := json.Marshal(map[string]any{"directory": root, "command": "printf 'partial evidence'; sleep 5", "timeoutSeconds": 1})
			delta = map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "timeout-call", "type": "function", "function": map[string]string{"name": "shell", "arguments": string(args)}}}}
			finishReason = "tool_calls"
		} else {
			last := request.Messages[len(request.Messages)-1]
			var result struct {
				ArtifactID, Preview, Error, OutputPath string
				Complete                               bool
				NextOffset                             int64
			}
			if json.Unmarshal([]byte(last.Content), &result) != nil || result.Preview != "partial evidence" || result.Error == "" || result.ArtifactID == "" || result.Complete || result.OutputPath != "" || result.NextOffset != 16 {
				t.Errorf("model lost partial shell output: %s", last.Content)
			}
			artifactID = result.ArtifactID
			delta = map[string]any{"content": "partial output preserved"}
		}
		encoded, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": delta, "finish_reason": finishReason}}})
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", encoded)
	}))
	defer upstream.Close()
	command := workerproto.Command{SessionID: "session", RunID: "run", Prompt: "run shell", Model: modelcfg.Model{Provider: "openai-chat", ID: "fixture", BaseURL: upstream.URL, APIKey: "fixture"}}
	failed := false
	answer, err := Run(context.Background(), command, func(event workerproto.Result) error {
		if event.Kind == "tool" && event.Status == "failed" && event.Tool.Name == "shell" {
			failed = event.Tool.OutputRef != nil && event.Tool.OutputRef.ArtifactID != ""
		}
		return record(event)
	})
	if err != nil || answer != "partial output preserved" || requests != 2 || !failed {
		t.Fatalf("partial output flow: %q, requests=%d, failed=%v, error=%v", answer, requests, failed, err)
	}
	output, err := readToolOutput(context.Background(), fmt.Sprintf(`{"artifactId":%q}`, artifactID), command)
	var page struct {
		Content  string
		Complete bool
	}
	if err != nil || json.Unmarshal([]byte(output), &page) != nil || page.Content != "partial evidence" || page.Complete {
		t.Fatalf("partial artifact recovery: %s, %v", output, err)
	}
}
