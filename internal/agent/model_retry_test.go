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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	modelcfg "vista/internal/model"
	"vista/internal/provider"
	"vista/internal/workerproto"
)

func TestModelRetryRetainsCompletedToolResultsWithoutRepeatingShell(t *testing.T) {
	t.Run("natural schedule", func(t *testing.T) { testModelRetryAfterShellResult(t, false) })
	t.Run("yielded shell", func(t *testing.T) { testModelRetryAfterShellResult(t, true) })
}

func testModelRetryAfterShellResult(t *testing.T, forceYield bool) {
	t.Helper()
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
	const runID, callID = "retry-tool", "write-once"
	const executionID = runID + ":tool:main/" + callID
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var mu sync.Mutex
	var requests, pendingRequests int
	var retryBody string
	var completedTools, retries, modelRequests atomic.Int32
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		encoded, _ := json.Marshal(body)
		mu.Lock()
		defer mu.Unlock()
		requests++
		if requests == 1 {
			args, _ := json.Marshal(map[string]any{"command": command, "directory": root, "timeoutSeconds": 5})
			continuationToolCalls(w, scriptedToolCall(callID, "shell", string(args)))
			return
		}
		if retryBody != "" {
			if requests != 3+pendingRequests || retryBody != string(encoded) {
				t.Errorf("retry changed context or exceeded count: %d", requests)
			}
			continuationAnswer(w, "done")
			return
		}
		var messages []provider.ChatMessage
		if err := json.Unmarshal(body["messages"], &messages); err != nil || len(messages) == 0 {
			t.Errorf("tool context missing: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		last := messages[len(messages)-1]
		if runtimeNoticeOnWire(last.Role, last.Content) {
			_, data, _ := strings.Cut(last.Content, "\n")
			var notice workerproto.RuntimeEvent
			if err := json.Unmarshal([]byte(data), &notice); err != nil || notice.ID != executionID || notice.RunID != runID || notice.Recipient != "main" || notice.Kind != "tool" || notice.SourceID != callID || notice.Status != "succeeded" {
				t.Errorf("wrong deferred shell completion: %+v, %v", notice, err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			last.Role, last.ToolCallID, last.Content = "tool", callID, notice.Content
		}
		var result struct {
			Status      string `json:"status"`
			ExecutionID string `json:"executionId"`
			ExitCode    *int   `json:"exitCode"`
			Complete    bool   `json:"complete"`
		}
		if err := json.Unmarshal([]byte(last.Content), &result); err != nil || last.Role != "tool" || last.ToolCallID != callID {
			t.Errorf("wrong shell result in model context: %+v, %v", last, err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if result.Status == "running" {
			if result.ExecutionID != executionID || pendingRequests != 0 {
				t.Errorf("wrong or repeated pending shell result: %+v", result)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			pendingRequests++
			continuationAnswer(w, "Waiting for the shell result")
			return
		}
		if !result.Complete || result.ExitCode == nil || *result.ExitCode != 0 {
			t.Errorf("retry must start after a successful shell result: %+v", result)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		retryBody = string(encoded)
		w.WriteHeader(http.StatusBadGateway)
		fmt.Fprint(w, `{"error":{"type":"server_error","message":"all pool attempts failed"}}`)
	}))
	defer endpoint.Close()
	answer, err := Run(ctx, workerproto.Command{RunID: runID, Prompt: "Write the marker once and report.", Model: modelcfg.Model{Provider: "openai-chat", ID: "fixture", BaseURL: endpoint.URL, APIKey: "key"}}, func(event workerproto.Result) error {
		if event.Kind == "tool" && event.Status == "succeeded" && event.Tool != nil && event.Tool.CallID == callID && event.Tool.Name == "shell" {
			if forceYield {
				// Hold publication beyond the ordinary yield boundary, not the shell itself.
				select {
				case <-time.After(2 * toolYieldDelay):
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			completedTools.Add(1)
		}
		if event.Kind == "model_retry" && event.Status == "waiting" {
			retries.Add(1)
		}
		if event.Kind == "model_request" {
			modelRequests.Add(1)
		}
		return nil
	})
	marker, readErr := os.ReadFile(filepath.Join(root, "once"))
	mu.Lock()
	defer mu.Unlock()
	if err != nil || readErr != nil || answer != "done" || string(marker) != "x" || requests != 3+pendingRequests || (forceYield && pendingRequests != 1) || completedTools.Load() != 1 || retries.Load() != 1 || modelRequests.Load() != int32(requests) {
		t.Fatalf("recovery: answer=%q marker=%q requests=%d waiting=%d tools=%d retries=%d modelRequests=%d err=%v readErr=%v", answer, marker, requests, pendingRequests, completedTools.Load(), retries.Load(), modelRequests.Load(), err, readErr)
	}
}
