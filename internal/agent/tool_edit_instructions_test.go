package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"vista/internal/agentconfig"
	"vista/internal/appdirs"
	"vista/internal/provider"
	"vista/internal/tools"
	"vista/internal/workerproto"
)

func TestInstructionToolEditsTheSameGlobalAndProjectDocuments(t *testing.T) {
	_, ctx := checkpointFixture(t)
	config, err := appdirs.Config()
	if err != nil {
		t.Fatal(err)
	}
	project := t.TempDir()
	runtime := &toolRuntime{}
	for _, scope := range []string{"global", "project"} {
		input := map[string]any{"action": "read", "scope": scope}
		directory := config
		if scope == "project" {
			input["directory"] = project
			directory = project
		}
		output, err := runtime.editInstructions(ctx, instructionCall(t, "instruction", "edit_instructions", input))
		if err != nil || !strings.Contains(output, `"sha256":"missing"`) {
			t.Fatalf("missing: %s %v", output, err)
		}
		input["action"], input["expectedSha256"], input["text"] = "append", "missing", "Use pnpm."
		if _, err := runtime.editInstructions(ctx, instructionCall(t, "instruction", "edit_instructions", input)); err != nil {
			t.Fatal(err)
		}
		snapshot, err := agentconfig.ReadSnapshot(directory)
		if err != nil || snapshot.Content != "Use pnpm.\n" {
			t.Fatal(snapshot, err)
		}
		input["action"], input["oldText"], input["text"], input["expectedSha256"] = "replace", "Use pnpm.", "Use npm.", snapshot.SHA256
		if _, err := runtime.editInstructions(ctx, instructionCall(t, "instruction", "edit_instructions", input)); err != nil {
			t.Fatal(err)
		}
		if _, err := runtime.editInstructions(ctx, instructionCall(t, "instruction", "edit_instructions", input)); !errors.Is(err, tools.ErrConflict) {
			t.Fatalf("stale write: %v", err)
		}
		snapshot, _ = agentconfig.ReadSnapshot(directory)
		input["oldText"], input["text"], input["expectedSha256"] = "Use npm.\n", "", snapshot.SHA256
		if _, err := runtime.editInstructions(ctx, instructionCall(t, "instruction", "edit_instructions", input)); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(filepath.Join(directory, "AGENTS.md"))
		if err != nil || len(data) != 0 {
			t.Fatalf("delete passage: %q %v", data, err)
		}
	}
}

func TestInstructionToolPreservesLongDocumentsAndRejectsAmbiguousEdits(t *testing.T) {
	_, ctx := checkpointFixture(t)
	project := t.TempDir()
	path := filepath.Join(project, "AGENTS.md")
	original := strings.Repeat("Keep this exact rule.\r\n", 60000) + "unique final rule"
	if err := os.WriteFile(path, []byte(original), 0640); err != nil {
		t.Fatal(err)
	}
	snapshot, _ := agentconfig.ReadSnapshot(project)
	runtime := &toolRuntime{}
	input := map[string]any{"action": "replace", "scope": "project", "directory": project, "expectedSha256": snapshot.SHA256, "oldText": "Keep this exact rule.", "text": "wrong"}
	if _, err := runtime.editInstructions(ctx, instructionCall(t, "instruction", "edit_instructions", input)); err == nil {
		t.Fatal("ambiguous replacement accepted")
	}
	input["oldText"] = "not present"
	if _, err := runtime.editInstructions(ctx, instructionCall(t, "instruction", "edit_instructions", input)); err == nil {
		t.Fatal("missing replacement accepted")
	}
	delete(input, "oldText")
	input["action"], input["text"] = "append", "New lasting requirement."
	if _, err := runtime.editInstructions(ctx, instructionCall(t, "instruction", "edit_instructions", input)); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != original+"\nNew lasting requirement.\n" {
		t.Fatal("append rewrote unrelated content")
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0640 {
		t.Fatalf("permissions changed: %v", info.Mode())
	}
}

func TestInstructionToolConcurrentEditsAndInvalidTargets(t *testing.T) {
	_, ctx := checkpointFixture(t)
	project := t.TempDir()
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, text := range []string{"First requirement.", "Second requirement."} {
		call := instructionCall(t, "instruction", "edit_instructions", map[string]any{"action": "append", "scope": "project", "directory": project, "expectedSha256": "missing", "text": text})
		wg.Add(1)
		go func() { defer wg.Done(); _, err := (&toolRuntime{}).editInstructions(ctx, call); results <- err }()
	}
	wg.Wait()
	close(results)
	succeeded, conflicted := 0, 0
	for err := range results {
		if err == nil {
			succeeded++
		} else if errors.Is(err, tools.ErrConflict) {
			conflicted++
		} else {
			t.Fatal(err)
		}
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("concurrent writes: %d/%d", succeeded, conflicted)
	}
	for _, input := range []map[string]any{
		{"action": "append", "scope": "project", "text": "x", "expectedSha256": "missing"},
		{"action": "append", "scope": "global", "directory": project, "text": "x", "expectedSha256": "missing"},
		{"action": "read", "scope": "project", "directory": project, "text": "x"},
		{"action": "append", "scope": "project", "directory": project, "text": " "},
		{"action": "replace", "scope": "project", "directory": project, "expectedSha256": "missing", "text": "x"},
	} {
		if _, err := (&toolRuntime{}).editInstructions(ctx, instructionCall(t, "instruction", "edit_instructions", input)); err == nil {
			t.Fatalf("invalid input accepted: %+v", input)
		}
	}
	call := instructionCall(t, "instruction", "edit_instructions", map[string]any{"action": "read", "scope": "project", "directory": project})
	if _, err := (&toolRuntime{depth: 1}).editInstructions(ctx, call); err == nil {
		t.Fatal("child instruction tool accepted")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := (&toolRuntime{}).execute(cancelled, call); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestInstructionToolCannotBypassScopedRuleAcknowledgment(t *testing.T) {
	_, ctx := checkpointFixture(t)
	project := t.TempDir()
	if err := os.WriteFile(filepath.Join(project, "AGENTS.md"), []byte("Read these rules first.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	snapshot, _ := agentconfig.ReadSnapshot(project)
	call := instructionCall(t, "instruction", "edit_instructions", map[string]any{"action": "append", "scope": "project", "directory": project, "expectedSha256": snapshot.SHA256, "text": "A new rule."})
	runtime := &toolRuntime{}
	output, err := runtime.execute(ctx, call)
	if err != nil || !strings.Contains(output, `"status":"instructions_required"`) {
		t.Fatalf("missing rule gate: %s %v", output, err)
	}
	unchanged, _ := agentconfig.ReadSnapshot(project)
	if unchanged.SHA256 != snapshot.SHA256 {
		t.Fatal("rule notice performed a write")
	}
	runtime.instructions.acknowledge([]provider.ChatMessage{{Role: "assistant", ToolCalls: []provider.ToolCall{call}}, {Role: "tool", ToolCallID: call.ID, Content: output}})
	if output, err := runtime.execute(ctx, call); err != nil || strings.Contains(output, "instructions_required") {
		t.Fatalf("acknowledged rule blocked: %s %v", output, err)
	}
}

func TestLongTermInstructionsRefreshAcrossExecutionsWithoutMemoryTool(t *testing.T) {
	_, ctx := checkpointFixture(t)
	for _, text := range []string{"Prefer metric units.", "Prefer imperial units."} {
		config, _ := appdirs.Config()
		if err := agentconfig.Write(config, text); err != nil {
			t.Fatal(err)
		}
		for _, depth := range []int{0, 1} {
			prepared, err := prepareContext(ctx, workerproto.Command{SessionID: "session", Prompt: "continue"}, nil, depth, 2)
			if err != nil || !strings.Contains(prepared.systemBase, text) || strings.Contains(prepared.systemBase, "saved preferences at run start") {
				t.Fatalf("current instructions missing: %v", err)
			}
			found := false
			for _, schema := range prepared.schemas {
				name := schema["function"].(map[string]any)["name"]
				if name == "memory" {
					t.Fatal("retired memory tool advertised")
				}
				found = found || name == "edit_instructions"
			}
			if found != (depth == 0) {
				t.Fatalf("instruction tool at depth %d: %v", depth, found)
			}
		}
	}
}

func TestInstructionToolReadPaginationAndLinkedTargets(t *testing.T) {
	_, ctx := checkpointFixture(t)
	project := t.TempDir()
	path := filepath.Join(project, "AGENTS.md")
	text := "First rule.\nSecond rule.\nThird rule.\n"
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	call := instructionCall(t, "read", "edit_instructions", map[string]any{"action": "read", "scope": "project", "directory": project, "startLine": 2, "lineCount": 1})
	output, err := (&toolRuntime{}).editInstructions(ctx, call)
	snapshot, _ := agentconfig.ReadSnapshot(project)
	if err != nil || !strings.Contains(output, "2: Second rule.") || strings.Contains(output, "First rule.") || !strings.Contains(output, snapshot.SHA256) {
		t.Fatalf("paged read lost content/version semantics: %s %v", output, err)
	}
	t.Run("linked file", func(t *testing.T) {
		linked := t.TempDir()
		if err := os.Symlink(path, filepath.Join(linked, "AGENTS.md")); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		call := instructionCall(t, "linked", "edit_instructions", map[string]any{"action": "append", "scope": "project", "directory": linked, "expectedSha256": snapshot.SHA256, "text": "Do not add this."})
		if _, err := (&toolRuntime{}).editInstructions(ctx, call); err == nil {
			t.Fatal("linked/generated copy accepted as a direct write target")
		}
		data, _ := os.ReadFile(path)
		if string(data) != text {
			t.Fatal("linked target modified")
		}
	})
}

func TestInstructionToolAppendPreservesSubmittedMarkdown(t *testing.T) {
	_, ctx := checkpointFixture(t)
	project := t.TempDir()
	const original = "# Instructions\n"
	const addition = "    command --flag  \n\n"
	if err := os.WriteFile(filepath.Join(project, "AGENTS.md"), []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := agentconfig.ReadSnapshot(project)
	if err != nil {
		t.Fatal(err)
	}
	call := instructionCall(t, "append", "edit_instructions", map[string]any{"action": "append", "scope": "project", "directory": project, "expectedSha256": snapshot.SHA256, "text": addition})
	if _, err := (&toolRuntime{}).editInstructions(ctx, call); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(project, "AGENTS.md"))
	if err != nil || string(data) != original+addition {
		t.Fatalf("append changed Markdown whitespace: %q %v", data, err)
	}
}
