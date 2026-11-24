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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"vista/internal/provider"
	"vista/internal/tools"
	"vista/internal/workerproto"
)

func instructionCall(t *testing.T, id, name string, arguments any) provider.ToolCall {
	t.Helper()
	encoded, err := json.Marshal(arguments)
	if err != nil {
		t.Fatal(err)
	}
	return scriptedToolCall(id, name, string(encoded))
}

func instructionMessages(t *testing.T, path string, offset int) []provider.ChatMessage {
	t.Helper()
	page, err := tools.ReadProjectInstruction(path, offset)
	if err != nil {
		t.Fatal(err)
	}
	output, err := marshalResult(page)
	if err != nil {
		t.Fatal(err)
	}
	call := instructionCall(t, fmt.Sprintf("read-%d", offset), "read_project_instruction", map[string]any{"path": path, "offset": offset})
	return []provider.ChatMessage{{Role: "assistant", ToolCalls: []provider.ToolCall{call}}, {Role: "tool", ToolCallID: call.ID, Content: output}}
}

func TestInstructionPagesReachTheModelWithoutAnotherTruncation(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, name := range []string{"read_project_instruction", "read_skill"} {
		t.Run(name, func(t *testing.T) {
			filename := "AGENTS.md"
			if name == "read_skill" {
				filename = "SKILL.md"
			}
			path := filepath.Join(t.TempDir(), filename)
			text := strings.Repeat("<\t\"\\中文🙂\n", 10000)
			if err := os.WriteFile(path, []byte(text), 0600); err != nil {
				t.Fatal(err)
			}
			runtime := &toolRuntime{}
			var recovered strings.Builder
			for offset := 0; ; {
				call := instructionCall(t, fmt.Sprint(offset), name, map[string]any{"path": path, "offset": offset})
				read := runtime.readProjectInstruction
				if name == "read_skill" {
					read = runtime.readSkill
				}
				output, err := read(context.Background(), call)
				if err != nil {
					t.Fatal(err)
				}
				bounded, err := boundToolOutput(context.Background(), "rules", call.ID, name, output, nil, func(workerproto.Result) error { return nil })
				if err != nil || bounded.model != output {
					t.Fatalf("page was truncated again: %v", err)
				}
				var page tools.SkillPage
				if err := json.Unmarshal([]byte(bounded.model), &page); err != nil || page.NextOffset <= offset || page.SHA256 == "" {
					t.Fatalf("page lost content or metadata: %v", err)
				}
				recovered.WriteString(page.Content)
				if !page.Truncated {
					break
				}
				offset = page.NextOffset
			}
			if recovered.String() != text {
				t.Fatal("model received incomplete instruction text")
			}
		})
	}
}

func requireInstructionNotice(t *testing.T, state *projectInstructionState, call provider.ToolCall, paths ...string) {
	t.Helper()
	output, err := state.prepare(context.Background(), call)
	if len(paths) == 0 {
		if output != "" || err != nil {
			t.Fatalf("operation unexpectedly blocked: %s %v", output, err)
		}
		return
	}
	var notice instructionNotice
	if err != nil || json.Unmarshal([]byte(output), &notice) != nil || notice.Status != "instructions_required" || notice.Executed || len(notice.Instructions) != len(paths) {
		t.Fatalf("missing rule notice: %s %v", output, err)
	}
	for i, path := range paths {
		if notice.Instructions[i].Path != path || notice.Instructions[i].SHA256 == "" {
			t.Fatalf("wrong rule order or missing version: %+v", notice)
		}
	}
}

func TestInstructionChecksDetectNewAndChangedScopedRules(t *testing.T) {
	root := t.TempDir()
	left, right := filepath.Join(root, "left"), filepath.Join(root, "right")
	for _, dir := range []string{left, right} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	call := instructionCall(t, "write", "write_file", map[string]string{"path": filepath.Join(left, "new.txt")})
	state := projectInstructionState{}
	requireInstructionNotice(t, &state, call)
	outer, inner := filepath.Join(root, "AGENTS.md"), filepath.Join(left, "CLAUDE.md")
	for path, text := range map[string]string{outer: "outer rule", inner: "inner rule"} {
		if err := os.WriteFile(path, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	requireInstructionNotice(t, &state, call, outer, inner)
	messages := instructionMessages(t, outer, 0)
	state.acknowledge(messages)
	requireInstructionNotice(t, &state, call, inner)
	messages = append(messages, instructionMessages(t, inner, 0)...)
	state.acknowledge(messages)
	requireInstructionNotice(t, &state, call)

	// Same-length edits must be detected even without an mtime change.
	info, err := os.Stat(inner)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(inner, []byte("other rule"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(inner, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	requireInstructionNotice(t, &state, call, inner)
	otherCall := instructionCall(t, "right", "search", map[string]string{"root": right})
	requireInstructionNotice(t, &state, otherCall)
	sibling := filepath.Join(right, "AGENTS.md")
	if err := os.WriteFile(sibling, []byte("sibling rule"), 0600); err != nil {
		t.Fatal(err)
	}
	requireInstructionNotice(t, &state, otherCall, sibling)
	if err := os.Remove(inner); err != nil {
		t.Fatal(err)
	}
	requireInstructionNotice(t, &state, call)
}

func TestInstructionCoverageUsesOnlyCompletePagesInConsumedContext(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "AGENTS.md")
	if err := os.WriteFile(path, []byte(strings.Repeat("规则\n", 24000)), 0600); err != nil {
		t.Fatal(err)
	}
	var pages [][]provider.ChatMessage
	for offset := 0; ; {
		pages = append(pages, instructionMessages(t, path, offset))
		var page tools.SkillPage
		if err := json.Unmarshal([]byte(pages[len(pages)-1][1].Content), &page); err != nil {
			t.Fatal(err)
		}
		if !page.Truncated {
			break
		}
		offset = page.NextOffset
	}
	if len(pages) < 3 {
		t.Fatal("fixture needs at least three pages")
	}
	call := instructionCall(t, "read", "read_file", map[string]string{"path": filepath.Join(dir, "target")})
	state := projectInstructionState{}
	messages := append([]provider.ChatMessage{}, pages[len(pages)-1]...)
	messages = append(messages, pages[0]...)
	state.acknowledge(messages)
	requireInstructionNotice(t, &state, call, path)
	for _, page := range pages[1 : len(pages)-1] {
		messages = append(messages, page...)
	}
	state.acknowledge(messages)
	requireInstructionNotice(t, &state, call)

	// A resumed run may reuse the actual retained reads, but another task or a
	// context summary without those pages must read the rules itself.
	resumed := projectInstructionState{}
	resumed.acknowledge(messages)
	requireInstructionNotice(t, &resumed, call)
	child := projectInstructionState{}
	requireInstructionNotice(t, &child, call, path)
	state.acknowledge([]provider.ChatMessage{{Role: "system", Content: "A summary mentions the rules."}})
	requireInstructionNotice(t, &state, call, path)

	// A preview, a missing tool-call pair, and a read with the wrong offset
	// cannot be treated as the original page.
	for _, kind := range []string{"preview", "orphan", "wrong offset"} {
		t.Run(kind, func(t *testing.T) {
			invalid := append([]provider.ChatMessage{}, messages...)
			switch kind {
			case "preview":
				invalid[1].Content = `{"preview":"partial rule text","truncated":true}`
			case "orphan":
				invalid = invalid[1:]
			case "wrong offset":
				invalid[0].ToolCalls = []provider.ToolCall{instructionCall(t, invalid[1].ToolCallID, "read_project_instruction", map[string]any{"path": path, "offset": 1})}
			}
			state.acknowledge(invalid)
			requireInstructionNotice(t, &state, call, path)
		})
	}
	if err := os.WriteFile(path, []byte(strings.Repeat("新规\n", 24000)), 0600); err != nil {
		t.Fatal(err)
	}
	state.acknowledge(append(messages, instructionMessages(t, path, 0)...))
	requireInstructionNotice(t, &state, call, path)
}

func TestInstructionChecksReuseTheCurrentGlobalPromptVersion(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "AGENTS.md")
	if err := os.WriteFile(path, []byte("old global instructions"), 0600); err != nil {
		t.Fatal(err)
	}
	old := instructionMessages(t, path, 0)
	if err := os.WriteFile(path, []byte("current global instructions"), 0600); err != nil {
		t.Fatal(err)
	}
	current, err := tools.ReadProjectInstruction(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	state := projectInstructionState{global: tools.InstructionFile{Path: path, SHA256: current.SHA256}}
	call := instructionCall(t, "read", "read_skill", map[string]string{"path": filepath.Join(dir, "SKILL.md")})
	state.acknowledge(old)
	requireInstructionNotice(t, &state, call)
	if err := os.WriteFile(path, []byte("changed during the run"), 0600); err != nil {
		t.Fatal(err)
	}
	requireInstructionNotice(t, &state, call, path)
	state.acknowledge(append(old, instructionMessages(t, path, 0)...))
	requireInstructionNotice(t, &state, call)
}

func TestFilesystemRuleChecksRunBeforeHandlers(t *testing.T) {
	dir := t.TempDir()
	rule := filepath.Join(dir, "AGENTS.md")
	if err := os.WriteFile(rule, []byte("scoped rules"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, field, target string
	}{
		{"read_file", "path", filepath.Join(dir, "file.txt")},
		{"write_file", "path", filepath.Join(dir, "file.txt")},
		{"search", "root", dir},
		{"shell", "directory", dir},
		{"start_background", "directory", dir},
		{"read_resource", "path", filepath.Join(dir, "image.png")},
		{"read_skill", "path", filepath.Join(dir, "SKILL.md")},
	} {
		t.Run(test.name, func(t *testing.T) {
			runtime := &toolRuntime{definitions: toolSet{{schema: map[string]any{"function": map[string]any{"name": test.name}}, execute: func(*toolRuntime, context.Context, provider.ToolCall) (string, error) {
				t.Error("handler ran before rules were consumed")
				return "", nil
			}}}}
			call := instructionCall(t, "attempt", test.name, map[string]string{test.field: test.target})
			output, err := runtime.execute(context.Background(), call)
			if err != nil || !strings.Contains(output, `"executed":false`) {
				t.Fatalf("missing unexecuted notice: %s %v", output, err)
			}
		})
	}
	for _, call := range []provider.ToolCall{
		instructionCall(t, "embedded", "read_skill", map[string]string{"path": "skill://system/vista-guide/SKILL.md"}),
		instructionCall(t, "hash", "read_resource", map[string]string{"hash": strings.Repeat("a", 64)}),
		instructionCall(t, "rules", "read_project_instruction", map[string]string{"path": rule}),
		instructionCall(t, "explicit", "project_instructions", map[string]string{"path": dir}),
	} {
		requireInstructionNotice(t, &projectInstructionState{}, call)
	}
}

func instructionTurn(w http.ResponseWriter, calls ...provider.ToolCall) {
	items := make([]any, len(calls))
	for i, call := range calls {
		items[i] = map[string]any{"index": i, "id": call.ID, "type": "function", "function": call.Function}
	}
	continuationEvent(w, map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": items}, "finish_reason": "tool_calls"}}})
}

func TestAgentExecutesWithoutAnEmptyInstructionRound(t *testing.T) {
	_, arguments := continuationFixture(t)
	var requests atomic.Int32
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			continuationEvent(w, mcpToolEvent("direct-read", "read_file", arguments))
			return
		}
		var body struct{ Messages []provider.ChatMessage }
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		last := body.Messages[len(body.Messages)-1]
		if last.Role != "tool" || !strings.Contains(last.Content, "continuation contents") {
			t.Errorf("file operation did not execute immediately: %+v", last)
		}
		continuationAnswer(w, "done")
	}))
	defer model.Close()
	answer, err := Run(context.Background(), continuationCommand(model.URL, "Read the file."), func(workerproto.Result) error { return nil })
	if err != nil || answer != "done" || requests.Load() != 2 {
		t.Fatalf("empty-rule operation: answer=%q requests=%d err=%v", answer, requests.Load(), err)
	}
}

func TestAgentConsumesDiscoveredRulesWithoutASeparateReadRound(t *testing.T) {
	for _, concurrentRead := range []bool{false, true} {
		for _, delayWrite := range []bool{false, true} {
			t.Run(fmt.Sprintf("concurrent explicit read=%t/deferred write=%t", concurrentRead, delayWrite), func(t *testing.T) {
				testAgentConsumesDiscoveredRules(t, concurrentRead, delayWrite)
			})
		}
	}
}

func testAgentConsumesDiscoveredRules(t *testing.T, concurrentRead, delayWrite bool) {
	t.Helper()
	const runID, blockedWrite, acceptedWrite, readRules = "rule-retry", "blocked-write", "after-reading-write", "read-rules"
	const rules = "Use the project writing rules."
	root, _ := continuationFixture(t)
	path, rule := filepath.Join(root, "created.txt"), filepath.Join(root, "AGENTS.md")
	if err := os.WriteFile(rule, []byte(rules), 0600); err != nil {
		t.Fatal(err)
	}
	args := map[string]string{"path": path, "expectedSha256": "missing", "content": "written after reading"}
	initial := []provider.ToolCall{instructionCall(t, blockedWrite, "write_file", args)}
	if concurrentRead {
		initial = append([]provider.ToolCall{instructionCall(t, readRules, "read_project_instruction", map[string]string{"path": rule})}, initial...)
	}
	next := instructionCall(t, acceptedWrite, "write_file", args)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	// The latest matching runtime notice supersedes a running tool reply.
	reply := func(messages []provider.ChatMessage, callID string) (string, bool, error) {
		executionID := runID + ":tool:main/" + callID
		for i := len(messages) - 1; i >= 0; i-- {
			msg := messages[i]
			if runtimeNoticeOnWire(msg.Role, msg.Content) {
				_, raw, _ := strings.Cut(msg.Content, "\n")
				var notice workerproto.RuntimeEvent
				if err := json.Unmarshal([]byte(raw), &notice); err != nil {
					return "", false, err
				}
				if notice.SourceID != callID {
					continue
				}
				if notice.ID != executionID || notice.Kind != "tool" || notice.Status != "succeeded" {
					return "", false, fmt.Errorf("wrong completion for %s", callID)
				}
				return notice.Content, false, nil
			}
			if msg.Role == "tool" && msg.ToolCallID == callID {
				var result struct {
					Status      string `json:"status"`
					ExecutionID string `json:"executionId"`
				}
				pending := json.Unmarshal([]byte(msg.Content), &result) == nil && result.Status == "running"
				if pending && result.ExecutionID != executionID {
					return "", false, fmt.Errorf("wrong pending result for %s", callID)
				}
				return msg.Content, pending, nil
			}
		}
		return "", false, fmt.Errorf("missing tool reply for %s", callID)
	}
	var mu sync.Mutex
	var requests, waitingTurns int
	afterReading, writeYielded := false, false
	calls := initial
	remaining := len(calls) + 1
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		requests++
		var body struct{ Messages []provider.ChatMessage }
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			http.Error(w, "invalid model context", http.StatusBadRequest)
			return
		}
		if !afterReading {
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Errorf("write ran before model consumed rules, request=%d err=%v", requests, err)
			}
		}
		if requests == 1 {
			instructionTurn(w, calls...)
			return
		}
		pending := 0
		var blockedResult string
		for _, call := range calls {
			content, running, err := reply(body.Messages, call.ID)
			if err != nil {
				t.Error(err)
				http.Error(w, "invalid tool response", http.StatusBadRequest)
				return
			}
			if running {
				pending++
			} else if call.ID == blockedWrite {
				blockedResult = content
			}
		}
		if pending > 0 {
			if pending >= remaining {
				t.Error("repeated waiting turn without a new tool completion")
				http.Error(w, "unexpected waiting turn", http.StatusBadRequest)
				return
			}
			remaining = pending
			waitingTurns++
			writeYielded = writeYielded || afterReading
			continuationAnswer(w, "Waiting for the existing tool calls")
			return
		}
		if !afterReading {
			var notice instructionNotice
			if err := json.Unmarshal([]byte(blockedResult), &notice); err != nil || notice.Status != "instructions_required" || notice.Executed || len(notice.Pages) != 1 || notice.Pages[0].Content != rules {
				t.Errorf("model did not receive the blocked result and actual rules: %+v %v", notice, err)
				http.Error(w, "invalid instructions notice", http.StatusBadRequest)
				return
			}
			afterReading = true
			calls = []provider.ToolCall{next}
			remaining = len(calls) + 1
			instructionTurn(w, next)
			return
		}
		if data, err := os.ReadFile(path); err != nil || string(data) != "written after reading" {
			t.Errorf("retry did not write: %q %v", data, err)
		}
		continuationAnswer(w, "done")
	}))
	defer model.Close()
	command := continuationCommand(model.URL, "Create the file.")
	command.RunID = runID
	var blocked, written atomic.Int32
	answer, err := Run(ctx, command, func(event workerproto.Result) error {
		if event.Kind == "tool" && event.Status == "succeeded" && event.Tool != nil {
			switch event.Tool.CallID {
			case blockedWrite:
				if strings.Contains(event.Content, `"status":"instructions_required"`) {
					blocked.Add(1)
				}
			case acceptedWrite:
				if delayWrite {
					// Force the normal yield path without changing the runtime.
					select {
					case <-time.After(2 * toolYieldDelay):
					case <-ctx.Done():
						return ctx.Err()
					}
				}
				written.Add(1)
			}
		}
		return nil
	})
	mu.Lock()
	defer mu.Unlock()
	if err != nil || answer != "done" || requests != 3+waitingTurns || blocked.Load() != 1 || written.Load() != 1 || (delayWrite && !writeYielded) {
		t.Fatalf("rule retry: answer=%q requests=%d waiting=%d blocked=%d written=%d yielded=%v err=%v", answer, requests, waitingTurns, blocked.Load(), written.Load(), writeYielded, err)
	}
}

func TestConcurrentRuleChecksShareOnlyPreviouslyConsumedReads(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "AGENTS.md")
	if err := os.WriteFile(path, []byte("project rules"), 0600); err != nil {
		t.Fatal(err)
	}
	state := projectInstructionState{}
	messages := instructionMessages(t, path, 0)
	call := instructionCall(t, "write", "write_file", map[string]string{"path": filepath.Join(dir, "file.txt")})
	for _, consumed := range []bool{false, true} {
		if consumed {
			state.acknowledge(messages)
		}
		var wg sync.WaitGroup
		for i := 0; i < 12; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if consumed {
					requireInstructionNotice(t, &state, call)
				} else {
					requireInstructionNotice(t, &state, call, path)
				}
			}()
		}
		wg.Wait()
	}
}
