package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"vista/internal/provider"
	"vista/internal/tools"
	"vista/internal/workerproto"
)

func TestInlineInstructionPagesFitAndContinueWithoutGaps(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dir := t.TempDir()
	path := filepath.Join(dir, "AGENTS.md")
	full := strings.Repeat("<\t\"\\中文🙂\n", 8000)
	if err := os.WriteFile(path, []byte(full), 0600); err != nil {
		t.Fatal(err)
	}
	state := projectInstructionState{}
	var messages []provider.ChatMessage
	var recovered strings.Builder
	for turn := 0; ; turn++ {
		if turn > 20 {
			t.Fatal("inline rule coverage did not converge")
		}
		call := instructionCall(t, fmt.Sprint(turn), "write_file", map[string]string{"path": filepath.Join(dir, "new.txt")})
		output, err := state.prepare(context.Background(), call)
		if err != nil {
			t.Fatal(err)
		}
		if output == "" {
			break
		}
		bounded, err := boundToolOutput(context.Background(), "run", call.ID, call.Function.Name, output, nil, func(workerproto.Result) error { return nil })
		if err != nil || bounded.model != output || exceedsToolBudget(output) {
			t.Fatalf("inline rules were previewed again: %v", err)
		}
		var notice instructionNotice
		if err := json.Unmarshal([]byte(output), &notice); err != nil || len(notice.Pages) != 1 {
			t.Fatalf("missing paged rules: %s %v", output, err)
		}
		page := notice.Pages[0]
		if page.Offset != recovered.Len() || page.NextOffset != page.Offset+len(page.Content) || len(page.Content) == 0 {
			t.Fatalf("rule page repeated or skipped bytes: %+v", page)
		}
		recovered.WriteString(page.Content)
		messages = append(messages, provider.ChatMessage{Role: "assistant", ToolCalls: []provider.ToolCall{call}}, provider.ChatMessage{Role: "tool", ToolCallID: call.ID, Content: output})
		state.acknowledge(messages)
	}
	if recovered.String() != full || len(state.inherited()) != 1 || state.inherited()[0].Content != full {
		t.Fatal("inline rules did not survive complete coverage/inheritance")
	}
	kept, err := retainedProjectInstructionResults(context.Background(), messages)
	if err != nil || len(kept) < 2 {
		t.Fatalf("inline rule pages are not retained through compaction: %v %v", kept, err)
	}
}

func TestInstructionNoticeVerifiesCompleteRuleText(t *testing.T) {
	for name, repetitions := range map[string]int{"whole": 1, "paged": 4000} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "AGENTS.md")
			content := strings.Repeat("Keep the original rule text.\n", repetitions)
			if err := os.WriteFile(path, []byte(content), 0600); err != nil {
				t.Fatal(err)
			}
			call := instructionCall(t, "write", "write_file", map[string]string{"path": filepath.Join(root, "out.txt")})
			state := projectInstructionState{}
			output, err := state.prepare(context.Background(), call)
			if err != nil {
				t.Fatal(err)
			}
			var notice instructionNotice
			if err := json.Unmarshal([]byte(output), &notice); err != nil || len(notice.Pages) != 1 {
				t.Fatalf("missing rule text: %v", err)
			}
			offset := notice.Pages[0].NextOffset
			notice.Pages[0].Content = strings.Repeat("x", len(notice.Pages[0].Content))
			altered, err := marshalResult(notice)
			if err != nil {
				t.Fatal(err)
			}
			messages := []provider.ChatMessage{{Role: "assistant", ToolCalls: []provider.ToolCall{call}}, {Role: "tool", ToolCallID: call.ID, Content: altered}}
			for offset < len(content) {
				pair := instructionMessages(t, path, offset)
				var page tools.SkillPage
				if err := json.Unmarshal([]byte(pair[1].Content), &page); err != nil || page.NextOffset <= offset {
					t.Fatalf("invalid next rule page: %v", err)
				}
				messages = append(messages, pair...)
				offset = page.NextOffset
			}
			state.acknowledge(messages)
			requireInstructionNotice(t, &state, call, path)
			// Re-reading the actual text repairs coverage instead of permanently
			// blocking the task because an earlier result contained bad text.
			messages = append(messages, provider.ChatMessage{Role: "assistant", ToolCalls: []provider.ToolCall{call}}, provider.ChatMessage{Role: "tool", ToolCallID: call.ID, Content: output})
			state.acknowledge(messages)
			requireInstructionNotice(t, &state, call)
		})
	}
}

func TestAsyncInstructionReadRestoresFullTextBeforeAcknowledging(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dir := t.TempDir()
	path := filepath.Join(dir, "AGENTS.md")
	if err := os.WriteFile(path, []byte(strings.Repeat("规则 ", 4000)), 0600); err != nil {
		t.Fatal(err)
	}
	page, err := tools.ReadProjectInstruction(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	output, _ := marshalResult(page)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	release := make(chan struct{})
	prepared := preparedContext{tools: toolSet{{schema: readProjectInstructionSchema, execute: func(_ *toolRuntime, ctx context.Context, _ provider.ToolCall) (string, error) {
		select {
		case <-release:
			return output, nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}}}}
	scope, err := newRunScope(nil)
	if err != nil {
		t.Fatal(err)
	}
	call := instructionCall(t, "slow-rule", "read_project_instruction", map[string]string{"path": path})
	var work sync.WaitGroup
	messages, err := executeToolBatch(ctx, workerproto.Command{RunID: "run", TaskID: "main"}, 1, []provider.ToolCall{call}, make([]error, 1), &prepared, nil, scope, 0, nil, nil, &work, func(workerproto.Result) error { return nil })
	close(release)
	work.Wait()
	if err != nil || len(messages) != 1 || !strings.Contains(messages[0].Content, `"status":"running"`) {
		t.Fatalf("fixture did not yield asynchronously: %+v %v", messages, err)
	}
	events := scope.events.local("main")
	if len(events) != 1 || events[0].ContentOutput == nil {
		t.Fatalf("large rule result lost its exact-output reference: %+v", events)
	}
	actual, err := runtimeMessage(events[0])
	if err != nil || !strings.Contains(actual.Content, page.Content) {
		t.Fatalf("rule text was clipped in runtime transport: %v", err)
	}
	state := projectInstructionState{}
	state.acknowledge(append([]provider.ChatMessage{{Role: "assistant", ToolCalls: []provider.ToolCall{call}}}, append(messages, actual)...))
	requireInstructionNotice(t, &state, instructionCall(t, "write", "write_file", map[string]string{"path": filepath.Join(dir, "new.txt")}))
}

func TestInlineRulesRequirePairedActualTextAndSupportRuntimeDelivery(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "AGENTS.md")
	if err := os.WriteFile(path, []byte("scoped rules"), 0600); err != nil {
		t.Fatal(err)
	}
	call := instructionCall(t, "write", "write_file", map[string]string{"path": filepath.Join(dir, "new.txt")})
	state := projectInstructionState{}
	output, err := state.prepare(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	var notice instructionNotice
	if err := json.Unmarshal([]byte(output), &notice); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"metadata", "wrong offset", "wrong hash", "orphan", "unrelated tool"} {
		t.Run(kind, func(t *testing.T) {
			var invalid instructionNotice
			_ = json.Unmarshal([]byte(output), &invalid)
			paired := call
			switch kind {
			case "metadata":
				invalid.Pages = nil
			case "wrong offset":
				invalid.Pages[0].Offset = 1
			case "wrong hash":
				invalid.Pages[0].SHA256 = strings.Repeat("0", 64)
			case "unrelated tool":
				paired.Function.Name = "webfetch"
			}
			text, _ := marshalResult(invalid)
			messages := []provider.ChatMessage{{Role: "assistant", ToolCalls: []provider.ToolCall{paired}}, {Role: "tool", ToolCallID: paired.ID, Content: text}}
			if kind == "orphan" {
				messages = messages[1:]
			}
			state.acknowledge(messages)
			requireInstructionNotice(t, &state, call, path)
		})
	}
	messages := []provider.ChatMessage{
		{Role: "assistant", ToolCalls: []provider.ToolCall{call}},
		{Role: "tool", ToolCallID: call.ID, Content: `{"status":"running"}`},
		runtimeNotice(workerproto.RuntimeEvent{Kind: "tool", SourceID: call.ID, Content: output}),
	}
	state.acknowledge(messages)
	requireInstructionNotice(t, &state, call)
	if err := os.WriteFile(path, []byte("updated rule"), 0600); err != nil {
		t.Fatal(err)
	}
	requireInstructionNotice(t, &state, call, path)
}

func TestAsyncInstructionRulesRemainAcknowledgedAfterCompaction(t *testing.T) {
	for _, name := range []string{"read_project_instruction", "write_file"} {
		t.Run(name, func(t *testing.T) {
			root, _ := continuationFixture(t)
			path := filepath.Join(root, "AGENTS.md")
			if err := os.WriteFile(path, []byte("Use the directory rules."), 0600); err != nil {
				t.Fatal(err)
			}
			page, err := tools.ReadProjectInstruction(path, 0)
			if err != nil {
				t.Fatal(err)
			}
			write := instructionCall(t, "write", "write_file", map[string]string{"path": filepath.Join(root, "new.txt")})
			call := instructionCall(t, "async-rules", name, map[string]string{"path": path})
			state := projectInstructionState{}
			output, err := marshalResult(page)
			if name == "write_file" {
				call.Function.Arguments = write.Function.Arguments
				output, err = state.prepare(context.Background(), call)
			}
			if err != nil {
				t.Fatal(err)
			}
			// Protect the exact originating call/result group, including native
			// replay state, while compacting unrelated completed work.
			origin := opaqueCompactionGroup(call.ID)
			origin[0].Content = strings.Repeat("Observations before reading directory rules. ", 500)
			origin[0].ToolCalls[0].Function = call.Function
			origin[1].Content = `{"status":"running"}`
			runtime := runtimeNotice(workerproto.RuntimeEvent{Kind: "tool", SourceID: call.ID, Content: output})
			unrelated := strings.Repeat("Unrelated completed work. ", 200)
			messages := []provider.ChatMessage{{Role: "system", Content: "system"}, {Role: "user", Content: "task"}}
			messages = append(messages, origin...)
			messages = append(messages, runtime,
				provider.ChatMessage{Role: "assistant", Content: unrelated},
				provider.ChatMessage{Role: "assistant", Content: "Latest answer."})
			state.acknowledge(messages)
			requireInstructionNotice(t, &state, write)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				continuationAnswer(w, "Summary of unrelated completed work.")
			}))
			defer upstream.Close()
			command := continuationCommand(upstream.URL, "task")
			compactor := newExecutionCompactor(messages)
			next, changed, err := compactor.compact(context.Background(), command, messages, nil, func(workerproto.Result) error { return nil }, &runScope{}, 0)
			if err != nil || !changed {
				t.Fatalf("compaction did not run: changed=%v err=%v", changed, err)
			}
			origins, completions := 0, 0
			for index, message := range next {
				if len(message.ToolCalls) == 1 && message.ToolCalls[0].ID == call.ID {
					origins++
					if index+1 >= len(next) || !reflect.DeepEqual(next[index:index+2], origin) {
						t.Error("compaction changed the originating assistant/tool pair or its native replay state")
					}
				}
				if reflect.DeepEqual(message, runtime) {
					completions++
				}
				if message.Content == unrelated {
					t.Error("unrelated history was not compacted")
				}
			}
			if origins != 1 || completions != 1 {
				t.Fatalf("compaction lost or duplicated asynchronous rule history: origins=%d completions=%d", origins, completions)
			}
			state.acknowledge(next)
			requireInstructionNotice(t, &state, write)
			if inherited := state.inherited(); len(inherited) != 1 || inherited[0].Content != page.Content {
				t.Fatalf("compaction lost exact inherited rules: %+v", inherited)
			}
			if err := os.WriteFile(path, []byte("Changed rules."), 0600); err != nil {
				t.Fatal(err)
			}
			requireInstructionNotice(t, &state, write, path)
		})
	}
}
