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

func TestReadSessionReadsOlderMessagesByPage(t *testing.T) {
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
	call.Function.Name = "read_session"
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

func TestReadSessionReadsFullCrossSessionAnswerWithoutOpening(t *testing.T) {
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
	const target = "00000000-0000-4000-8000-000000000011"
	if _, err := db.Create(ctx, target, "ARCHIVED work"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.BeginRun(ctx, target, "old", "old-user", "private prompt"); err != nil {
		t.Fatal(err)
	}
	part := strings.Repeat("🧪", 500)
	for range 6 {
		if _, err := db.AppendModelDelta(ctx, target, "old", 1, 0, part, "main"); err != nil {
			t.Fatal(err)
		}
	}
	event, err := db.FinishRun(ctx, target, "old", "old-answer", "succeeded", "short preview", store.ModelOutputRef{Turn: 1, Bytes: len(part) * 6})
	if err != nil {
		t.Fatal(err)
	}
	var saved struct {
		Message store.Message `json:"message"`
	}
	if err := json.Unmarshal(event.Data, &saved); err != nil || saved.Message.Sequence < 2 {
		t.Fatalf("bad fixture: %s %v", event.Data, err)
	}
	if err := db.SetOpen(ctx, target, false); err != nil {
		t.Fatal(err)
	}
	r := &toolRuntime{parent: workerproto.Command{SessionID: "source", MessageSequence: 1}}
	out, err := r.execute(ctx, scriptedToolCall("list", "list_sessions", `{"title":"archived","open":false,"updatedBefore":"2999-01-01T00:00:00Z"}`))
	if err != nil || !strings.Contains(out, target) || strings.Contains(out, "private prompt") || strings.Contains(out, "short preview") {
		t.Fatalf("metadata only: %s %v", out, err)
	}
	out, err = r.readSession(ctx, scriptedToolCall("browse", "read_session", `{"sessionId":"`+target+`"}`))
	if err != nil || !strings.Contains(out, "short preview") {
		t.Fatalf("browse archived: %s %v", out, err)
	}
	var text strings.Builder
	for offset := 0; offset < len(part)*6; {
		input, _ := json.Marshal(map[string]any{"sessionId": target, "sequence": saved.Message.Sequence, "offset": offset, "limit": 4097})
		out, err = r.readSession(ctx, scriptedToolCall("body", "read_session", string(input)))
		var page struct {
			SessionID, Content     string
			NextOffset, TotalBytes int
		}
		if err != nil || json.Unmarshal([]byte(out), &page) != nil || page.SessionID != target || page.NextOffset <= offset || page.TotalBytes != len(part)*6 {
			t.Fatalf("page at %d: %v %.250s", offset, err, out)
		}
		text.WriteString(page.Content)
		offset = page.NextOffset
	}
	if text.String() != strings.Repeat(part, 6) {
		t.Fatal("canonical answer bytes were lost")
	}
	if state, err := db.Get(ctx, target); err != nil || state.Open {
		t.Fatalf("archived conversation was reopened: %+v %v", state, err)
	}
}
