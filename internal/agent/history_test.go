package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"vista/internal/provider"
	"vista/internal/store"
	"vista/internal/workerproto"
)

func TestHistoryToolsReadOlderMessagesByPage(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
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
	const session = "00000000-0000-4000-8000-000000000010"
	if _, err := db.Create(ctx, session, "long conversation"); err != nil {
		t.Fatal(err)
	}
	oldText := "retain this constraint: " + strings.Repeat("a", 3000)
	if _, _, err := db.BeginRun(ctx, session, "old", "old-user", oldText); err != nil {
		t.Fatal(err)
	}
	if _, err := db.FinishRun(ctx, session, "old", "old-answer", "succeeded", "noted"); err != nil {
		t.Fatal(err)
	}
	current, _, err := db.BeginRun(ctx, session, "current", "current-user", "what was the constraint?")
	if err != nil {
		t.Fatal(err)
	}
	parent := workerproto.Command{SessionID: session, MessageSequence: current.Sequence}
	call := provider.ToolCall{}
	call.Function.Name = "history_index"
	call.Function.Arguments = `{}`
	output, err := (&toolRuntime{parent: parent}).execute(ctx, call)
	if err != nil {
		t.Fatal(err)
	}
	var index struct {
		Messages []struct {
			Sequence int64  `json:"sequence"`
			Preview  string `json:"preview"`
		} `json:"messages"`
	}
	if err := json.Unmarshal([]byte(output), &index); err != nil || len(index.Messages) != 2 || index.Messages[1].Sequence != 1 || !strings.Contains(index.Messages[1].Preview, "retain this constraint") {
		t.Fatalf("history index: %s %v", output, err)
	}
	call.Function.Name = "read_history"
	call.Function.Arguments = `{"sequence":1}`
	output, err = (&toolRuntime{parent: parent}).execute(ctx, call)
	if err != nil {
		t.Fatal(err)
	}
	var page struct {
		Content    string `json:"content"`
		NextOffset int    `json:"nextOffset"`
		TotalBytes int    `json:"totalBytes"`
	}
	if err := json.Unmarshal([]byte(output), &page); err != nil || !strings.HasPrefix(page.Content, "retain this constraint") || page.NextOffset <= 0 || page.NextOffset >= page.TotalBytes {
		t.Fatalf("history page: %s %v", output, err)
	}
	call.Function.Arguments = `{"sequence":4}`
	if _, err := (&toolRuntime{parent: parent}).execute(ctx, call); err == nil {
		t.Fatal("current prompt was readable through older history tool")
	}
}
