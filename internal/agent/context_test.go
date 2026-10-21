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
	"runtime"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"vista/internal/agentconfig"
	modelcfg "vista/internal/model"
	"vista/internal/platform"
	"vista/internal/provider"
	"vista/internal/resources"
	"vista/internal/store"
	"vista/internal/workerproto"
)

func TestPreparedContextRetainsTaskInstructionsHistoryAndToolOrder(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	config := filepath.Join(root, "vista")
	state := filepath.Join(config, "state")
	skills := filepath.Join(config, "skills")
	for _, dir := range []string{state, skills} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := agentconfig.Write(config, "Keep user constraints."); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(filepath.Join(state, "vista.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if _, err := db.Create(ctx, "context-session", "context fixture"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.BeginRun(ctx, "context-session", "old-run", "old-user", "Remember the path."); err != nil {
		t.Fatal(err)
	}
	if _, err := db.FinishRun(ctx, "context-session", "old-run", "old-answer", "succeeded", "Noted."); err != nil {
		t.Fatal(err)
	}
	_, _, err = db.BeginRun(ctx, "context-session", "current-run", "current-user", "Continue.")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ReplaceChecklist(ctx, "context-session", "current-run", []store.ChecklistItem{{Text: "Inspect", Status: "done"}, {Text: "Verify", Status: "pending"}}); err != nil {
		t.Fatal(err)
	}
	always := []string{"read_file", "read_tool_output", "search", "webfetch", "websearch", "write_file", "shell", "start_background", "stop_background", "list_skills", "read_skill", "project_instructions", "read_project_instruction", "history_index", "read_history", "list_resources", "read_resource", "read_model_output"}
	for _, tc := range []struct {
		name  string
		depth int
		mcp   bool
	}{
		{"main with MCP", 0, true},
		{"child with MCP and delegation", 1, true},
		{"leaf without MCP", 2, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			command := workerproto.Command{
				SessionID: "context-session", RunID: "current-run", Prompt: "Continue.",
				History: []workerproto.Message{{Role: "user", Content: "Visible user message.", Images: []workerproto.Image{{Hash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", MediaType: "image/png", Bytes: 13}}}, {Role: "assistant", Content: "Visible answer."}},
				Images:  []workerproto.Image{{Hash: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", MediaType: "image/jpeg", Bytes: 13}},
			}
			if tc.mcp {
				command.MCPServers = []workerproto.MCPServer{{ID: "files", Name: "files"}, {ID: "search", Name: "search"}}
			}
			prepared, err := prepareContext(ctx, command, func(event workerproto.Result) error {
				return fmt.Errorf("cached history unexpectedly emitted %s", event.Kind)
			}, tc.depth, 2)
			if err != nil {
				t.Fatal(err)
			}
			wantSystem := systemPrompt + "\nRuntime OS: " + runtime.GOOS + "; shell: " + platform.ShellName() + ".\n\nUser's global instructions:\nKeep user constraints."
			if tc.depth > 0 {
				wantSystem += delegatedReportPrompt
			}
			wantSystem += "\n\nUser's saved preferences at run start:\n[]\nTreat these as defaults; the current request and user's global instructions take precedence. Successful memory edits in this run supersede this snapshot; earlier runs' memory tool results are historical."
			wantSystem += "\nCurrent run checklist snapshot; later update_checklist tool results supersede it: [{\"text\":\"Inspect\",\"status\":\"done\"},{\"text\":\"Verify\",\"status\":\"pending\"}]"
			wantSystem += " User skill directory: " + skills + ". Discover only if relevant."
			wantTools := slices.Clone(always)
			if tc.depth == 0 {
				wantTools = append(wantTools, "update_checklist", "memory", "question")
			}
			if tc.mcp {
				wantSystem += " Configured MCP servers: [{\"id\":\"files\",\"name\":\"files\"},{\"id\":\"search\",\"name\":\"search\"}]. Use the server id in MCP tool arguments. Discover tools before calling them."
				wantTools = append(wantTools, "mcp_list_tools", "mcp_call_tool")
			}
			if tc.depth < 2 {
				wantSystem += delegationPrompt
				wantTools = append(wantTools, "delegate", "await_agent", "cancel_agent")
			}
			wantMessages := []provider.ChatMessage{
				{Role: "system", Content: wantSystem},
				{Role: "user", Content: "Visible user message.", Resources: []resources.Ref{{Hash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", MediaType: "image/png", Bytes: 13}}},
				{Role: "assistant", Content: "Visible answer."},
				{Role: "user", Content: "Continue.", Resources: []resources.Ref{{Hash: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", MediaType: "image/jpeg", Bytes: 13}}},
			}
			wantMessages[len(wantMessages)-1] = wantMessages[len(wantMessages)-1].WithVisualInput(true)
			if !reflect.DeepEqual(prepared.messages, wantMessages) || prepared.systemBase != wantSystem {
				t.Fatalf("context messages or prompt bytes changed: %#v", prepared.messages)
			}
			var gotTools []string
			for _, schema := range prepared.schemas {
				gotTools = append(gotTools, schema["function"].(map[string]any)["name"].(string))
			}
			if !slices.Equal(gotTools, wantTools) {
				t.Fatalf("tool visibility/order changed: got %v want %v", gotTools, wantTools)
			}
		})
	}
}

func TestPreparedContextDoesNotInheritEarlierRunChecklist(t *testing.T) {
	for _, status := range []string{"succeeded", "failed", "interrupted"} {
		t.Run(status, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("XDG_CONFIG_HOME", root)
			state := filepath.Join(root, "vista", "state")
			if err := os.MkdirAll(state, 0700); err != nil {
				t.Fatal(err)
			}
			db, err := store.Open(filepath.Join(state, "vista.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			ctx := context.Background()
			if _, err := db.Create(ctx, "s", "checklist isolation"); err != nil {
				t.Fatal(err)
			}
			if _, _, err := db.BeginRun(ctx, "s", "old", "old-user", "Previous task"); err != nil {
				t.Fatal(err)
			}
			if _, err := db.ReplaceChecklist(ctx, "s", "old", []store.ChecklistItem{{Text: "Stale checklist item", Status: "in_progress"}}); err != nil {
				t.Fatal(err)
			}
			if _, err := db.FinishRun(ctx, "s", "old", "old-reply", status, "Previous run ended"); err != nil {
				t.Fatal(err)
			}
			if _, _, err := db.BeginRun(ctx, "s", "new", "new-user", "New task"); err != nil {
				t.Fatal(err)
			}
			prepared, err := prepareContext(ctx, workerproto.Command{SessionID: "s", RunID: "new", Prompt: "New task"}, func(workerproto.Result) error { return nil }, 0, 0)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(prepared.messages[0].Content, "Stale checklist item") || strings.Contains(prepared.messages[0].Content, "checklist snapshot") {
				t.Fatal("new run inherited an earlier checklist in its system prompt", prepared.messages[0].Content)
			}
			if items, err := db.RunChecklist(ctx, "s", "old"); err != nil || len(items) != 1 || items[0].Status != "in_progress" {
				t.Fatal("historical checklist was changed", items, err)
			}
		})
	}
}

func TestInterjectionReappliesContextForModelToolCapability(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var requests atomic.Int32
	var initialSystem string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model    string
			Messages []struct{ Role, Content string }
			Tools    []json.RawMessage
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.Messages) == 0 {
			t.Errorf("invalid model request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		turn := requests.Add(1)
		if turn == 1 {
			initialSystem = body.Messages[0].Content
		}
		if turn == 2 {
			if body.Model != "text-only" || len(body.Tools) != 0 || !strings.Contains(body.Messages[0].Content, "This model cannot call tools.") {
				t.Errorf("text-only model retained tool capability: %+v", body)
			}
		} else if len(body.Tools) == 0 || body.Messages[0].Content != initialSystem {
			t.Errorf("tool-capable model did not restore original context at turn %d", turn)
		}
		continuationAnswer(w, fmt.Sprintf("answer %d", turn))
	}))
	defer upstream.Close()
	model := modelcfg.Model{Provider: "openai-chat", ID: "with-tools", BaseURL: upstream.URL, APIKey: "fixture"}
	inputs := make(chan workerproto.Command, 1)
	command := workerproto.Command{RunID: "capability-run", Prompt: "First request.", Model: model}
	answer, err := RunWithInputs(context.Background(), command, func(event workerproto.Result) error {
		if event.Kind != "model_result" || event.Turn > 2 {
			return nil
		}
		next := model
		if event.Turn == 1 {
			noTools := false
			next.ID, next.Tools = "text-only", &noTools
		}
		inputs <- workerproto.Command{RunID: command.RunID, InputID: fmt.Sprint(event.Turn), Prompt: "Continue with selected model.", Model: next}
		return nil
	}, inputs)
	if err != nil || answer != "answer 3" || requests.Load() != 3 {
		t.Fatalf("capability switches: answer=%q requests=%d error=%v", answer, requests.Load(), err)
	}
}
