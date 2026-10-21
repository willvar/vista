package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	modelcfg "vista/internal/model"
	"vista/internal/store"
	"vista/internal/workerproto"
)

// Every fixture uses a separate configuration/state root and a local mock model.
func continuationFixture(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	path := filepath.Join(root, "note.txt")
	if err := os.WriteFile(path, []byte("continuation contents"), 0600); err != nil {
		t.Fatal(err)
	}
	arguments, err := json.Marshal(map[string]string{"path": path})
	if err != nil {
		t.Fatal(err)
	}
	return root, string(arguments)
}

func continuationEvent(w http.ResponseWriter, event any) {
	encoded, _ := json.Marshal(event)
	w.Header().Set("Content-Type", "text/event-stream")
	fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", encoded)
}

func continuationAnswer(w http.ResponseWriter, answer string) {
	continuationEvent(w, map[string]any{"choices": []any{map[string]any{"delta": map[string]string{"content": answer}, "finish_reason": "stop"}}})
}

func continuationCommand(url, prompt string) workerproto.Command {
	return workerproto.Command{RunID: "continuation-run", Prompt: prompt, Model: modelcfg.Model{Provider: "openai-chat", ID: "scripted", BaseURL: url, APIKey: "test-key"}}
}

func TestAgentContinuesBeyondToolAndTurnBudgets(t *testing.T) {
	_, arguments := continuationFixture(t)
	var requests atomic.Int32
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		turn := requests.Add(1)
		if turn <= 32 {
			continuationEvent(w, mcpToolEvent(fmt.Sprintf("read-%d", turn), "read_file", arguments))
			return
		}
		continuationAnswer(w, "all reads complete")
	}))
	defer model.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	reads := 0
	answer, err := Run(ctx, continuationCommand(model.URL, "read repeatedly"), func(result workerproto.Result) error {
		if result.Kind == "tool" && result.Status == "succeeded" && result.Tool.Name == "read_file" {
			if !strings.Contains(result.Content, "continuation contents") {
				return fmt.Errorf("unexpected read result: %s", result.Content)
			}
			reads++
		}
		return nil
	})
	if err != nil || answer != "all reads complete" || reads != 32 || requests.Load() != 33 {
		t.Fatalf("long continuation: answer=%q reads=%d requests=%d err=%v", answer, reads, requests.Load(), err)
	}
}

func TestParentAndDelegateContinueBeyondSharedToolBudget(t *testing.T) {
	_, arguments := continuationFixture(t)
	var parentRequests, childRequests atomic.Int32
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if len(body.Messages) < 2 {
			t.Error("missing conversation")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if body.Messages[1].Content == "child task" {
			turn := childRequests.Add(1)
			if turn <= 14 {
				continuationEvent(w, mcpToolEvent(fmt.Sprintf("child-read-%d", turn), "read_file", arguments))
			} else {
				continuationAnswer(w, "child complete")
			}
			return
		}
		turn := parentRequests.Add(1)
		switch {
		case turn <= 12:
			continuationEvent(w, mcpToolEvent(fmt.Sprintf("parent-read-%d", turn), "read_file", arguments))
		case turn == 13:
			continuationEvent(w, mcpToolEvent("delegate-child", "delegate", `{"task":"child task"}`))
		case turn == 14:
			continuationEvent(w, mcpToolEvent("collect-child", "await_agent", `{"taskIds":["main/delegate-child"]}`))
		default:
			if !strings.Contains(body.Messages[len(body.Messages)-1].Content, "child complete") {
				t.Errorf("delegate result was not returned to parent: %+v", body.Messages[len(body.Messages)-1])
			}
			continuationAnswer(w, "parent complete")
		}
	}))
	defer model.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	reads := map[string]int{}
	answer, err := Run(ctx, continuationCommand(model.URL, "parent task"), func(result workerproto.Result) error {
		if result.Kind == "tool" && result.Status == "succeeded" && strings.HasSuffix(result.Tool.Name, "read_file") {
			reads[result.Tool.Name]++
		}
		return nil
	})
	if err != nil || answer != "parent complete" || reads["read_file"] != 12 || reads["subagent/read_file"] != 14 || parentRequests.Load() != 15 || childRequests.Load() != 15 {
		t.Fatalf("shared continuation: answer=%q reads=%v requests=%d/%d err=%v", answer, reads, parentRequests.Load(), childRequests.Load(), err)
	}
}

func TestAgentContinuesBeyondChecklistUpdateBudget(t *testing.T) {
	root, _ := continuationFixture(t)
	stateDir := filepath.Join(root, "vista", "state")
	if err := os.MkdirAll(stateDir, 0700); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(filepath.Join(stateDir, "vista.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Create(context.Background(), "session", "checklist"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.BeginRun(context.Background(), "session", "continuation-run", "user-message", "keep checklist current"); err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int32
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		turn := requests.Add(1)
		if turn <= 12 {
			arguments := fmt.Sprintf(`{"items":[{"text":"Step %d","status":"in_progress"}]}`, turn)
			continuationEvent(w, mcpToolEvent(fmt.Sprintf("checklist-%d", turn), "update_checklist", arguments))
			return
		}
		continuationAnswer(w, "checklist updated")
	}))
	defer model.Close()
	command := continuationCommand(model.URL, "keep checklist current")
	command.SessionID = "session"
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	updates := 0
	answer, err := Run(ctx, command, func(result workerproto.Result) error {
		if result.Kind != "checklist" {
			return nil
		}
		items := make([]store.ChecklistItem, len(result.Checklist.Items))
		for i, item := range result.Checklist.Items {
			items[i] = store.ChecklistItem{Text: item.Text, Status: item.Status}
		}
		if event, err := db.ReplaceChecklist(ctx, command.SessionID, command.RunID, items); err != nil || event == nil {
			return fmt.Errorf("persist checklist: event=%v err=%v", event, err)
		}
		updates++
		return nil
	})
	if err != nil || answer != "checklist updated" || updates != 12 || requests.Load() != 13 {
		t.Fatalf("checklist continuation: answer=%q updates=%d requests=%d err=%v", answer, updates, requests.Load(), err)
	}
	items, err := db.RunChecklist(context.Background(), command.SessionID, command.RunID)
	if err != nil || len(items) != 1 || items[0].Text != "Step 12" {
		t.Fatalf("final checklist: items=%+v err=%v", items, err)
	}
}

func TestChecklistUpdateMayRunAlongsideOtherTools(t *testing.T) {
	root, arguments := continuationFixture(t)
	stateDir := filepath.Join(root, "vista", "state")
	if err := os.MkdirAll(stateDir, 0700); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(filepath.Join(stateDir, "vista.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if _, err := db.Create(ctx, "session", "checklist"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.BeginRun(ctx, "session", "continuation-run", "user-message", "use the tools"); err != nil {
		t.Fatal(err)
	}

	var requests atomic.Int32
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			continuationEvent(w, map[string]any{"choices": []any{map[string]any{
				"delta": map[string]any{"tool_calls": []any{
					map[string]any{"index": 0, "id": "checklist", "type": "function", "function": map[string]string{"name": "update_checklist", "arguments": `{"items":[{"text":"Inspect the source","status":"in_progress"}]}`}},
					map[string]any{"index": 1, "id": "read", "type": "function", "function": map[string]string{"name": "read_file", "arguments": arguments}},
				}},
				"finish_reason": "tool_calls",
			}}})
			return
		}
		var body struct {
			Messages []struct {
				Role string `json:"role"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		toolResults := 0
		for _, message := range body.Messages {
			if message.Role == "tool" {
				toolResults++
			}
		}
		if toolResults != 2 {
			t.Errorf("mixed checklist turn returned %d tool results", toolResults)
		}
		continuationAnswer(w, "both tools completed")
	}))
	defer model.Close()

	command := continuationCommand(model.URL, "use the tools")
	command.SessionID = "session"
	checklist, read := false, false
	answer, err := Run(ctx, command, func(result workerproto.Result) error {
		if result.Kind == "checklist" {
			checklist = true
			items := make([]store.ChecklistItem, len(result.Checklist.Items))
			for i, item := range result.Checklist.Items {
				items[i] = store.ChecklistItem{Text: item.Text, Status: item.Status}
			}
			_, err := db.ReplaceChecklist(ctx, command.SessionID, command.RunID, items)
			return err
		}
		if result.Kind == "tool" && result.Status == "succeeded" && result.Tool != nil && result.Tool.Name == "read_file" {
			read = true
		}
		return nil
	})
	if err != nil || answer != "both tools completed" || !checklist || !read || requests.Load() != 2 {
		t.Fatalf("mixed checklist turn: answer=%q checklist=%v read=%v requests=%d err=%v", answer, checklist, read, requests.Load(), err)
	}
}

func TestContinuousToolLoopStopsOnContextCancellation(t *testing.T) {
	_, arguments := continuationFixture(t)
	var requests atomic.Int32
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		turn := requests.Add(1)
		continuationEvent(w, mcpToolEvent(fmt.Sprintf("read-%d", turn), "read_file", arguments))
	}))
	defer model.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	reads, modelEvents := 0, 0
	answer, err := Run(ctx, continuationCommand(model.URL, "continue until cancelled"), func(result workerproto.Result) error {
		if result.Kind == "model_request" {
			modelEvents++
		}
		if result.Kind == "tool" && result.Status == "succeeded" {
			reads++
			if reads == 3 {
				cancel()
			}
		}
		return nil
	})
	if !errors.Is(err, context.Canceled) || answer != "" || reads != 3 || requests.Load() != 3 || modelEvents != 3 {
		t.Fatalf("cancelled continuation: answer=%q reads=%d requests=%d modelEvents=%d err=%v", answer, reads, requests.Load(), modelEvents, err)
	}
}

func TestAgentExecutesTwelveToolsConcurrently(t *testing.T) {
	continuationFixture(t)
	const parallelCalls = 12
	var entered atomic.Int32
	allEntered := make(chan struct{})
	remote := mcp.NewServer(&mcp.Implementation{Name: "parallel-fixture", Version: "1"}, nil)
	remote.AddTool(&mcp.Tool{Name: "wait", InputSchema: json.RawMessage(`{"type":"object"}`)}, func(ctx context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if entered.Add(1) == parallelCalls {
			close(allEntered)
		}
		select {
		case <-allEntered:
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "concurrent tool complete"}}}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	})
	remoteHTTP := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return remote }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true}))
	defer remoteHTTP.Close()
	var requests atomic.Int32
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			calls := make([]map[string]any, parallelCalls)
			for index := range calls {
				calls[index] = map[string]any{"index": index, "id": fmt.Sprintf("parallel-%d", index), "type": "function", "function": map[string]string{
					"name": "mcp_call_tool", "arguments": `{"server":"parallel","name":"wait","arguments":{}}`,
				}}
			}
			continuationEvent(w, map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": calls}, "finish_reason": "tool_calls"}}})
			return
		}
		var body struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		results := 0
		for _, message := range body.Messages {
			if message.Role == "tool" && strings.Contains(message.Content, "concurrent tool complete") {
				results++
			}
		}
		if results != parallelCalls {
			t.Errorf("completed parallel tools returned to model: %d", results)
		}
		continuationAnswer(w, "all concurrent calls complete")
	}))
	defer model.Close()
	command := continuationCommand(model.URL, "run twelve independent tools")
	command.MCPServers = []workerproto.MCPServer{{ID: "parallel", Name: "parallel", Transport: "http", Endpoint: remoteHTTP.URL}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	completed := 0
	answer, err := Run(ctx, command, func(result workerproto.Result) error {
		if result.Kind == "tool" && result.Status == "succeeded" {
			completed++
		}
		return nil
	})
	if err != nil || answer != "all concurrent calls complete" || entered.Load() != parallelCalls || completed != parallelCalls || requests.Load() != 2 {
		t.Fatalf("parallel tools: answer=%q entered=%d completed=%d requests=%d err=%v", answer, entered.Load(), completed, requests.Load(), err)
	}
}
