package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	modelcfg "vista/internal/model"
	"vista/internal/workerproto"
)

func TestModelRetryRetainsCompletedToolResultsWithoutRepeatingShell(t *testing.T) {
	root := t.TempDir()
	command := "printf x >> once"
	switch runtime.GOOS {
	case "linux":
		t.Setenv("XDG_CONFIG_HOME", root)
	case "windows":
		t.Setenv("AppData", root)
		command = "[System.IO.File]::AppendAllText('once', 'x')"
	default:
		t.Skip("isolated config override requires Linux or Windows")
	}
	var requests, completedTools, retries, modelRequests int
	var retryBody string
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		encoded, _ := json.Marshal(body)
		requests++
		if requests == 2 {
			retryBody = string(encoded)
			if !strings.Contains(retryBody, `"role":"tool"`) {
				t.Error("completed tool result missing")
			}
			w.WriteHeader(502)
			fmt.Fprint(w, `{"error":{"type":"server_error","message":"all pool attempts failed"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if requests == 1 {
			args, _ := json.Marshal(map[string]any{"command": command, "directory": root, "timeoutSeconds": 5})
			chunk, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "write-once", "type": "function", "function": map[string]any{"name": "shell", "arguments": string(args)}}}}, "finish_reason": "tool_calls"}}})
			fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", chunk)
			return
		}
		if requests != 3 || retryBody != string(encoded) {
			t.Errorf("retry changed context or exceeded count: %d", requests)
		}
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"done\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer endpoint.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	answer, err := Run(ctx, workerproto.Command{RunID: "retry-tool", Prompt: "Write the marker once and report.", Model: modelcfg.Model{Provider: "openai-chat", ID: "fixture", BaseURL: endpoint.URL, APIKey: "key"}}, func(event workerproto.Result) error {
		if event.Kind == "tool" && event.Status == "succeeded" && event.Tool.Name == "shell" {
			completedTools++
		}
		if event.Kind == "model_retry" && event.Status == "waiting" {
			retries++
		}
		if event.Kind == "model_request" {
			modelRequests++
		}
		return nil
	})
	marker, readErr := os.ReadFile(filepath.Join(root, "once"))
	if err != nil || readErr != nil || answer != "done" || string(marker) != "x" || requests != 3 || completedTools != 1 || retries != 1 || modelRequests != 3 {
		t.Fatalf("recovery: answer=%q marker=%q requests=%d tools=%d retries=%d modelRequests=%d err=%v readErr=%v", answer, marker, requests, completedTools, retries, modelRequests, err, readErr)
	}
}
