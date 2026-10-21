package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"unicode/utf8"

	modelcfg "vista/internal/model"
	"vista/internal/workerproto"
)

func TestDelegateRetainsLongReportAndTaskIdentity(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	full := strings.Repeat("分析结果包含证据。", 900) + "完整报告末尾"
	commentary := strings.Repeat("Working on the requested analysis. ", 400)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []struct{ Role, Content string } `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		requests.Add(1)
		var delta any
		finishReason := "stop"
		if request.Messages[1].Content == "child analysis" {
			delta = map[string]any{"content": full}
		} else if request.Messages[len(request.Messages)-1].Role == "user" {
			finishReason = "tool_calls"
			delta = map[string]any{"content": commentary, "tool_calls": []any{map[string]any{"index": 0, "id": "delegate-1", "type": "function", "function": map[string]string{"name": "delegate", "arguments": `{"task":"child analysis"}`}}}}
		} else {
			last := request.Messages[len(request.Messages)-1]
			var started delegationResult
			if json.Unmarshal([]byte(last.Content), &started) == nil && started.Status == "running" {
				continuationEvent(w, mcpToolEvent("collect-report", "await_agent", `{"taskIds":["main/delegate-1"]}`))
				return
			}
			var result struct {
				Answer string                 `json:"answer"`
				Report *workerproto.OutputRef `json:"report"`
			}
			if json.Unmarshal([]byte(last.Content), &result) != nil || result.Answer != full || result.Report == nil || result.Report.Bytes != int64(len(full)) {
				t.Errorf("parent lost the successful full child report (%d bytes)", len(last.Content))
			}
			if request.Messages[2].Content != commentary {
				t.Error("text accompanying tools was lost")
			}
			delta = map[string]any{"content": "analysis complete"}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		encoded, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": delta, "finish_reason": finishReason}}})
		fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", encoded)
	}))
	defer server.Close()
	var events []workerproto.Result
	answer, err := Run(context.Background(), workerproto.Command{RunID: "task-run", Prompt: "main", Model: modelcfg.Model{Provider: "openai-chat", ID: "fixture", BaseURL: server.URL, APIKey: "fixture"}}, func(event workerproto.Result) error { events = append(events, event); return nil })
	if err != nil || answer != "analysis complete" || requests.Load() != 4 {
		t.Fatalf("report flow: answer=%q, requests=%d, error=%v", answer, requests.Load(), err)
	}
	var report *workerproto.OutputRef
	var childText strings.Builder
	started, completed := false, false
	for _, event := range events {
		if event.Kind == "agent_task" && event.Task.ID == "main/delegate-1" {
			if event.Task.ParentID != "main" || event.Task.Title != "child analysis" {
				t.Fatalf("task identity: %+v", event.Task)
			}
			if event.Task.Status == "started" {
				started = true
			}
			if event.Task.Status == "succeeded" {
				completed, report = true, event.Task.Report
			}
		}
		if event.TaskID == "main/delegate-1" && event.Kind == "model_delta" {
			if event.Depth != 1 || event.ParentTaskID != "main" {
				t.Fatalf("child event lost identity: %+v", event)
			}
			childText.WriteString(event.Content)
		}
		if event.Kind == "model_request" && event.Turn <= 0 {
			t.Error("ordinary request has no turn")
		}
		if event.Kind == "tool" && event.Status == "succeeded" && event.Tool.Name == "await_agent" {
			if event.Tool.OutputRef == nil || event.Tool.OutputRef.Bytes != int64(len(full)) {
				t.Fatalf("delegate report reference missing: %+v", event.Tool)
			}
			encoded, _ := json.Marshal(event)
			if len(encoded) > 32<<10 {
				t.Fatalf("tool event exceeds existing bound: %d bytes", len(encoded))
			}
		}
	}
	if !started || !completed || report == nil || report.Turn <= 0 || childText.String() != full {
		t.Fatal("child report or lifecycle incomplete")
	}
}

func TestToolOutputPreviewPreservesFullOutputAndStatus(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, full := range []string{strings.Repeat("完整\n", 2200), strings.Repeat("<\x00🙂", 10000)} {
		var events []workerproto.Result
		bounded, err := boundToolOutput(context.Background(), "run", "tool-1", "shell", full, nil, func(event workerproto.Result) error { events = append(events, event); return nil })
		if err != nil || !bounded.truncated || bounded.reference == nil {
			t.Fatalf("output preview: error=%v, ref=%+v", err, bounded.reference)
		}
		data, err := os.ReadFile(bounded.reference.OutputPath)
		if err != nil || string(data) != full {
			t.Fatalf("full output lost: bytes=%d, error=%v", len(data), err)
		}
		if !utf8.ValidString(bounded.event) || len(bounded.model) > maxToolResult {
			t.Fatal("preview exceeded model budget or broke UTF-8")
		}
		encoded, _ := json.Marshal(workerproto.Result{RunID: "run", Kind: "tool", Status: "succeeded", Content: bounded.event, Tool: &workerproto.ToolProgress{CallID: "tool-1", OutputRef: bounded.reference, Truncated: bounded.truncated}})
		if len(encoded) > 32<<10 {
			t.Fatalf("escaped event exceeds existing bound: %d", len(encoded))
		}
		if len(events) != 2 || events[0].Status != "started" || events[1].Status != "succeeded" || !events[1].Artifact.Complete {
			t.Fatal("successful output became a failure")
		}
	}
	encoded, _ := json.Marshal(map[string]string{"content": strings.Repeat("一行\n", 2100)})
	bounded, err := boundToolOutput(context.Background(), "run", "lines", "shell", string(encoded), nil, func(workerproto.Result) error { return nil })
	if err != nil || !bounded.truncated {
		t.Fatalf("JSON text did not respect the line budget: %v", err)
	}
	var modelPreview struct {
		Preview string `json:"preview"`
	}
	if json.Unmarshal([]byte(bounded.model), &modelPreview) != nil || strings.Count(modelPreview.Preview, "\n") >= maxToolResultLines {
		t.Fatal("JSON text preview exceeds line budget")
	}
}

func TestLongCollectedReportRetainsTaskIdentityInModelPreview(t *testing.T) {
	for _, answer := range []string{strings.Repeat("evidence\n", 2200), strings.Repeat("证据", 10000)} {
		ref := &workerproto.OutputRef{RunID: "run", Turn: 7, Bytes: int64(len(answer))}
		full, err := marshalResult(delegationResult{TaskID: "main/fast", Status: "succeeded", Answer: answer, Report: ref, AlreadyCollected: true})
		if err != nil {
			t.Fatal(err)
		}
		bounded, err := boundToolOutput(context.Background(), "run", "collect", "await_agent", full, ref, func(workerproto.Result) error { return nil })
		var preview struct {
			delegationResult
			OutputRef *workerproto.OutputRef `json:"outputRef"`
			Truncated bool                   `json:"truncated"`
		}
		if err != nil || json.Unmarshal([]byte(bounded.model), &preview) != nil {
			t.Fatalf("report preview failed: %v", err)
		}
		if !preview.Truncated || len(bounded.model) > maxToolResult || preview.TaskID != "main/fast" || preview.Status != "succeeded" || !preview.AlreadyCollected || preview.OutputRef == nil || *preview.OutputRef != *ref {
			t.Fatalf("long report lost identity or recovery reference: %+v", preview)
		}
	}
}
