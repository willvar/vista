package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"vista/internal/provider"
	"vista/internal/workerproto"
)

func TestToolCompletionPublishesBeforeBatchAndSlowResultSettlesOnlyOnce(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	release := make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	fast := make(chan struct{})
	var fastOnce sync.Once
	scope, err := newRunScope(nil)
	if err != nil {
		t.Fatal(err)
	}
	tool := func(name string, handler func(*toolRuntime, context.Context, provider.ToolCall) (string, error)) toolDefinition {
		return toolDefinition{schema: map[string]any{"function": map[string]any{"name": name}}, execute: handler}
	}
	prepared := preparedContext{tools: toolSet{
		tool("slow", func(_ *toolRuntime, ctx context.Context, _ provider.ToolCall) (string, error) {
			select {
			case <-release:
				return "slow evidence", nil
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}),
		tool("fast", func(_ *toolRuntime, _ context.Context, _ provider.ToolCall) (string, error) {
			return "fast evidence", nil
		}),
	}}
	var work sync.WaitGroup
	var mu sync.Mutex
	var completions []workerproto.Result
	emit := func(result workerproto.Result) error {
		mu.Lock()
		defer mu.Unlock()
		completions = append(completions, result)
		if result.Kind == "tool" && result.Status == "succeeded" && result.Tool.Name == "fast" {
			fastOnce.Do(func() { close(fast) })
		}
		return nil
	}
	calls := []provider.ToolCall{scriptedToolCall("slow-call", "slow", `{}`), scriptedToolCall("fast-call", "fast", `{}`)}
	done := make(chan []provider.ChatMessage, 1)
	failure := make(chan error, 1)
	go func() {
		messages, err := executeToolBatch(ctx, workerproto.Command{RunID: "run", TaskID: "main"}, 1, calls, make([]error, 2), &prepared, nil, scope, 0, nil, nil, &work, emit)
		failure <- err
		done <- messages
	}()
	select {
	case <-fast:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("fast completion was delayed by the slow tool")
	}
	messages := <-done
	if err := <-failure; err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 || !strings.Contains(messages[0].Content, `"status":"running"`) || messages[1].Content != "fast evidence" {
		t.Fatalf("one result per call: %+v", messages)
	}
	close(release)
	work.Wait()
	events := scope.events.local("main")
	if len(events) != 1 || events[0].Content != "slow evidence" || events[0].SourceID != "slow-call" {
		t.Fatalf("asynchronous result: %+v", events)
	}
	count := 0
	for _, event := range completions {
		if event.Kind == "tool" && event.Tool.CallID == "slow-call" && event.Status == "succeeded" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("duplicate terminal tool states: %d", count)
	}
}

func TestNewInputWakesIdleParentWithoutCancelingTwoChildren(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	release := make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	idle, acknowledged := make(chan struct{}), make(chan struct{})
	var idleOnce, ackOnce sync.Once
	var children, canceled, parents atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct{ Messages []workerproto.Message }
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		prompt := request.Messages[1].Content
		if strings.HasPrefix(prompt, "child ") {
			children.Add(1)
			select {
			case <-release:
				continuationAnswer(w, prompt+" report")
			case <-r.Context().Done():
				canceled.Add(1)
			}
			return
		}
		if parents.Add(1) == 1 {
			calls := []any{}
			for _, id := range []string{"a", "b"} {
				calls = append(calls, map[string]any{"index": len(calls), "id": id, "type": "function", "function": map[string]string{"name": "delegate", "arguments": `{"task":"child ` + id + `"}`}})
			}
			continuationEvent(w, map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": calls}, "finish_reason": "tool_calls"}}})
			return
		}
		all, changed := 0, false
		for _, message := range request.Messages {
			if message.Role == "user" && message.Content == "change focus" {
				changed = true
			}
			if message.Role == "system" && strings.Contains(message.Content, `"kind":"agent"`) {
				all++
			}
		}
		if changed {
			ackOnce.Do(func() { close(acknowledged) })
		}
		if all == 2 {
			continuationAnswer(w, "final including both reports")
		} else {
			continuationAnswer(w, "Waiting for reports")
		}
	}))
	defer server.Close()
	inputs := make(chan workerproto.Command, 1)
	done := make(chan error, 1)
	go func() {
		answer, err := RunWithInputs(ctx, continuationCommand(server.URL, "main"), func(result workerproto.Result) error {
			if result.Kind == "reply_checkpoint" {
				idleOnce.Do(func() { close(idle) })
			}
			return nil
		}, inputs)
		if err == nil && answer != "final including both reports" {
			t.Errorf("final answer: %q", answer)
		}
		done <- err
	}()
	select {
	case <-idle:
	case <-ctx.Done():
		t.Fatal("parent never became idle")
	}
	inputs <- workerproto.Command{RunID: "continuation-run", InputID: "input", Prompt: "change focus"}
	select {
	case <-acknowledged:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("new user input remained blocked behind child tasks")
	}
	if children.Load() != 2 || canceled.Load() != 0 {
		t.Fatalf("children before release: started=%d canceled=%d", children.Load(), canceled.Load())
	}
	releaseOnce.Do(func() { close(release) })
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestDelegatedRulesContainActualTextAndRevalidateChanges(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "AGENTS.md")
	text := "Use the project conventions.\n"
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	parent := projectInstructionState{}
	parent.acknowledge(instructionMessages(t, path, 0))
	rules := parent.inherited()
	if len(rules) != 1 || rules[0].Content != text {
		t.Fatalf("actual rules: %+v", rules)
	}
	encoded, _ := json.Marshal(rules)
	child := projectInstructionState{}
	inheritedMessages := []provider.ChatMessage{{Role: "system", Content: "Base instructions"}, {Role: "system", Content: inheritedRulesPrefix + string(encoded)}}
	child.acknowledge(inheritedMessages)
	kept, err := retainedProjectInstructionResults(context.Background(), inheritedMessages)
	if err != nil || len(kept) != 1 || !kept[1] {
		t.Fatalf("inherited rules retained an unrelated source message: %v %v", kept, err)
	}
	call := instructionCall(t, "read", "read_file", map[string]string{"path": filepath.Join(root, "file.go")})
	if notice, err := child.prepare(context.Background(), call); err != nil || notice != "" {
		t.Fatalf("inherited text required another read: %s %v", notice, err)
	}
	if err := os.WriteFile(path, []byte("Rules changed.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	requireInstructionNotice(t, &child, call, path)
	forged := []workerproto.ProjectRule{{Path: path, SHA256: rules[0].SHA256, Content: "Not the acknowledged text"}}
	encoded, _ = json.Marshal(forged)
	child.acknowledge([]provider.ChatMessage{{Role: "system", Content: inheritedRulesPrefix + string(encoded)}})
	requireInstructionNotice(t, &child, call, path)
}

func TestRuntimeContinuationKeepsToolTurnTextBeforeEmptyFinalReply(t *testing.T) {
	_, arguments := continuationFixture(t)
	var requests int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Messages []provider.ChatMessage }
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		users := 0
		for _, message := range body.Messages {
			if message.Role == "user" {
				users++
			}
		}
		if users != 1 {
			t.Errorf("continuation invented a user message: %+v", body.Messages)
		}
		requests++
		if requests == 1 {
			continuationEvent(w, map[string]any{"choices": []any{map[string]any{
				"delta": map[string]any{"content": "Checking the completed background work.", "tool_calls": []any{map[string]any{
					"index": 0, "id": "inspect", "type": "function", "function": map[string]string{"name": "read_file", "arguments": arguments},
				}}}, "finish_reason": "tool_calls",
			}}})
			return
		}
		continuationAnswer(w, "")
	}))
	defer upstream.Close()
	command := continuationCommand(upstream.URL, "Do not append this as a user message")
	command.Action = "resume"
	command.History = []workerproto.Message{{Role: "user", Content: "Original task"}}
	var replies []workerproto.CompletedReply
	answer, err := Run(context.Background(), command, func(result workerproto.Result) error {
		if result.Kind == "reply_checkpoint" && result.Reply != nil {
			replies = append(replies, *result.Reply)
		}
		return nil
	})
	if err != nil || answer != "" || requests != 2 || len(replies) != 1 || replies[0].Content != "Checking the completed background work." || replies[0].Turn != 1 {
		t.Fatalf("continuation lost visible text: answer=%q replies=%+v requests=%d err=%v", answer, replies, requests, err)
	}
}

func TestRuntimeContinuationDelegatesWithActualChildTask(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var parents, children atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Messages []provider.ChatMessage }
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		if len(body.Messages) < 2 {
			t.Error("delegated continuation omitted its task prompt")
			continuationAnswer(w, "missing task")
			return
		}
		if body.Messages[1].Content == "Inspect the completed background result" {
			children.Add(1)
			continuationAnswer(w, "Verified background report")
			return
		}
		if parents.Add(1) == 1 {
			continuationEvent(w, mcpToolEvent("inspect-child", "delegate", `{"task":"Inspect the completed background result"}`))
			return
		}
		for _, message := range body.Messages {
			if message.RuntimeData || message.Role == "system" && strings.Contains(message.Content, "Verified background report") {
				continuationAnswer(w, "Completed using the child report")
				return
			}
		}
		continuationAnswer(w, "Waiting for the child report")
	}))
	defer upstream.Close()
	command := continuationCommand(upstream.URL, "No new user prompt")
	command.Action = "resume"
	command.History = []workerproto.Message{{Role: "user", Content: "Original task"}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	answer, err := Run(ctx, command, func(workerproto.Result) error { return nil })
	if err != nil || answer != "Completed using the child report" || children.Load() != 1 || parents.Load() > 3 {
		t.Fatalf("delegated continuation: answer=%q parents=%d children=%d err=%v", answer, parents.Load(), children.Load(), err)
	}
}
