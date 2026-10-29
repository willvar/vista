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
	if err := os.WriteFile(path, []byte(strings.Repeat("规则\n", 700)), 0600); err != nil {
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
	if err := os.WriteFile(path, []byte(strings.Repeat("新规\n", 700)), 0600); err != nil {
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

func TestAgentWaitsForModelToConsumeRulesBeforeWriting(t *testing.T) {
	root, _ := continuationFixture(t)
	path, rule := filepath.Join(root, "created.txt"), filepath.Join(root, "AGENTS.md")
	if err := os.WriteFile(rule, []byte("Use the project writing rules."), 0600); err != nil {
		t.Fatal(err)
	}
	write := instructionCall(t, "blocked-write", "write_file", map[string]string{"path": path, "expectedSha256": "missing", "content": "written after reading"})
	read := instructionCall(t, "read-rules", "read_project_instruction", map[string]string{"path": rule})
	var requests atomic.Int32
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		turn := requests.Add(1)
		var body struct{ Messages []provider.ChatMessage }
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if turn <= 3 {
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Errorf("write ran before model consumed rules, turn=%d err=%v", turn, err)
			}
		}
		switch turn {
		case 1:
			instructionTurn(w, write)
		case 2:
			var notice instructionNotice
			if err := json.Unmarshal([]byte(body.Messages[len(body.Messages)-1].Content), &notice); err != nil || notice.Status != "instructions_required" || notice.Executed {
				t.Errorf("model did not receive the blocked result: %+v %v", notice, err)
			}
			write.ID = "same-turn-write"
			instructionTurn(w, read, write)
		case 3:
			encoded, _ := json.Marshal(body.Messages)
			if !strings.Contains(string(encoded), "Use the project writing rules.") {
				t.Error("model did not receive rule text")
			}
			write.ID = "after-reading-write"
			instructionTurn(w, write)
		default:
			if data, err := os.ReadFile(path); err != nil || string(data) != "written after reading" {
				t.Errorf("retry did not write: %q %v", data, err)
			}
			continuationAnswer(w, "done")
		}
	}))
	defer model.Close()
	blocked := 0
	answer, err := Run(context.Background(), continuationCommand(model.URL, "Create the file."), func(event workerproto.Result) error {
		if event.Kind == "tool" && event.Status == "succeeded" && strings.Contains(event.Content, `"status":"instructions_required"`) {
			blocked++
		}
		return nil
	})
	if err != nil || answer != "done" || requests.Load() != 4 || blocked != 2 {
		t.Fatalf("rule retry: answer=%q requests=%d blocked=%d err=%v", answer, requests.Load(), blocked, err)
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
