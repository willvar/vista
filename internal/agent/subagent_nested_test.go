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

	"vista/internal/agentconfig"
	"vista/internal/workerproto"
)

func TestParallelSubagentsCanDelegateNestedWork(t *testing.T) {
	configRoot := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configRoot)
	configDir := filepath.Join(configRoot, "vista")
	if err := os.Mkdir(configDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := agentconfig.WriteSettings(configDir, agentconfig.Settings{DelegationDepth: 2}); err != nil {
		t.Fatal(err)
	}
	const children, leavesPerChild = 3, 2
	const finalAnswer = "all nested reports returned"
	allLeavesEntered := make(chan struct{})
	var enteredLeaves atomic.Int32
	expectedReports := map[string]string{}
	expectedParents := map[string]string{"main": ""}
	childReports := map[string]string{}
	for child := range children {
		childID := fmt.Sprintf("main/child-%d", child)
		expectedParents[childID] = "main"
		var reports []string
		for leaf := range leavesPerChild {
			report := fmt.Sprintf("leaf %d/%d report", child, leaf)
			callID := fmt.Sprintf("%s/leaf-%d", childID, leaf)
			expectedReports[callID] = report
			expectedParents[callID] = childID
			reports = append(reports, report)
		}
		childReports[fmt.Sprintf("child %d", child)] = strings.Join(reports, " | ")
		expectedReports[childID] = strings.Join(reports, " | ")
	}
	readReports := func(messages []struct{ Role, Content string }, want []string) {
		t.Helper()
		got := map[string]bool{}
		for _, message := range messages {
			if message.Role != "tool" {
				continue
			}
			var output struct {
				Answer string
				Report *workerproto.OutputRef
			}
			if err := json.Unmarshal([]byte(message.Content), &output); err != nil {
				t.Errorf("decode delegated report: %v; content=%s", err, message.Content)
				continue
			}
			if output.Report == nil && output.Answer == "" {
				continue // Starting a delegated task returns only its handle.
			}
			if output.Report == nil || output.Report.RunID != "nested-run" || output.Report.Turn <= 0 || output.Report.Bytes != int64(len(output.Answer)) {
				t.Errorf("report reference does not match full answer: %+v", output)
			}
			got[output.Answer] = true
		}
		if len(got) != len(want) {
			t.Errorf("parent received %d reports, want %d: %+v", len(got), len(want), got)
		}
		for _, report := range want {
			if !got[report] {
				t.Errorf("parent did not receive report %q", report)
			}
		}
	}
	delegate := func(index int, id, task string) map[string]any {
		arguments, _ := json.Marshal(map[string]string{"task": task})
		return map[string]any{"index": index, "id": id, "type": "function", "function": map[string]string{"name": "delegate", "arguments": string(arguments)}}
	}
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []struct{ Role, Content string }
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || len(request.Messages) < 2 {
			t.Errorf("invalid model request: error=%v messages=%+v", err, request.Messages)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		prompt := request.Messages[1].Content
		initial := request.Messages[len(request.Messages)-1].Role == "user"
		var calls []map[string]any
		if !initial {
			var last delegationResult
			_ = json.Unmarshal([]byte(request.Messages[len(request.Messages)-1].Content), &last)
			if last.Status == "running" {
				for _, message := range request.Messages {
					var started delegationResult
					if message.Role != "tool" || json.Unmarshal([]byte(message.Content), &started) != nil || started.Status != "running" {
						continue
					}
					arguments, _ := json.Marshal(map[string]any{"taskIds": []string{started.TaskID}})
					calls = append(calls, map[string]any{"index": len(calls), "id": fmt.Sprintf("collect-%d", len(calls)), "type": "function", "function": map[string]string{"name": "await_agent", "arguments": string(arguments)}})
				}
				continuationEvent(w, map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": calls}, "finish_reason": "tool_calls"}}})
				return
			}
		}
		switch {
		case prompt == "main":
			if !initial {
				var reports []string
				for child := range children {
					reports = append(reports, childReports[fmt.Sprintf("child %d", child)])
				}
				readReports(request.Messages, reports)
				continuationAnswer(w, finalAnswer)
				return
			}
			for child := range children {
				calls = append(calls, delegate(child, fmt.Sprintf("child-%d", child), fmt.Sprintf("child %d", child)))
			}
		case strings.HasPrefix(prompt, "child "):
			var child int
			if _, err := fmt.Sscanf(prompt, "child %d", &child); err != nil || child < 0 || child >= children {
				t.Errorf("unexpected child prompt %q", prompt)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if !initial {
				readReports(request.Messages, []string{fmt.Sprintf("leaf %d/0 report", child), fmt.Sprintf("leaf %d/1 report", child)})
				continuationAnswer(w, childReports[prompt])
				return
			}
			for leaf := range leavesPerChild {
				calls = append(calls, delegate(leaf, fmt.Sprintf("leaf-%d", leaf), fmt.Sprintf("leaf %d/%d", child, leaf)))
			}
		case strings.HasPrefix(prompt, "leaf "):
			if enteredLeaves.Add(1) == children*leavesPerChild {
				close(allLeavesEntered)
			}
			// Holding a shared delegate slot while waiting on descendants would
			// prevent all six requests from reaching this barrier.
			select {
			case <-allLeavesEntered:
				continuationAnswer(w, prompt+" report")
			case <-r.Context().Done():
			}
			return
		default:
			t.Errorf("unexpected prompt %q", prompt)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		continuationEvent(w, map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": calls}, "finish_reason": "tool_calls"}}})
	}))
	defer model.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var events []workerproto.Result
	command := continuationCommand(model.URL, "main")
	command.RunID = "nested-run"
	answer, err := Run(ctx, command, func(event workerproto.Result) error {
		events = append(events, event)
		return nil
	})
	if err != nil || answer != finalAnswer || enteredLeaves.Load() != children*leavesPerChild {
		t.Fatalf("nested delegation: answer=%q leaves=%d error=%v", answer, enteredLeaves.Load(), err)
	}
	turns := map[int32]bool{}
	started, succeeded := map[string]bool{}, map[string]bool{}
	completedDelegates := map[string]bool{}
	for _, event := range events {
		switch event.Kind {
		case "model_request":
			if event.Turn <= 0 || turns[event.Turn] {
				t.Errorf("model request has nonunique turn %d", event.Turn)
			}
			turns[event.Turn] = true
		case "agent_task":
			parent, ok := expectedParents[event.Task.ID]
			if !ok || event.Task.ParentID != parent || event.TaskID != event.Task.ID || event.ParentTaskID != parent {
				t.Errorf("incorrect task ancestry: %+v", event)
			}
			if event.Task.Status == "started" {
				started[event.Task.ID] = true
			} else if event.Task.Status == "succeeded" {
				succeeded[event.Task.ID] = true
			} else {
				t.Errorf("task did not succeed: %+v", event.Task)
			}
		case "tool":
			if event.Status == "started" {
				continue
			}
			if event.Status != "succeeded" || event.Tool == nil {
				t.Errorf("delegate failed: %+v", event)
				continue
			}
			if strings.HasSuffix(event.Tool.Name, "delegate") {
				completedDelegates[event.Tool.CallID] = true
				continue
			}
			var output delegationResult
			if err := json.Unmarshal([]byte(event.Content), &output); err != nil || expectedReports[output.TaskID] != output.Answer {
				t.Errorf("delegate result missing report: call=%s answer=%q error=%v", event.Tool.CallID, output.Answer, err)
			}
		}
	}
	if len(turns) != 3+children*3+children*leavesPerChild || len(completedDelegates) != children+children*leavesPerChild {
		t.Errorf("incomplete execution: unique turns=%d delegates=%d", len(turns), len(completedDelegates))
	}
	for id := range expectedParents {
		if !started[id] || !succeeded[id] {
			t.Errorf("task %s is missing lifecycle events: started=%v succeeded=%v", id, started[id], succeeded[id])
		}
	}
}
