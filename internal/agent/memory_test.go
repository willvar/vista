package agent

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"
	"vista/internal/provider"
	"vista/internal/store"
	"vista/internal/workerproto"
)

func TestSavedPreferencesLoadAcrossSessionsAndRefreshAtExecutionBoundary(t *testing.T) {
	db, ctx := checkpointFixture(t)
	id := uuid.NewString()
	change := workerproto.MemoryChange{Action: "add", ID: id, Text: "Use metric units."}
	if err := db.ChangeMemory(ctx, change); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Create(ctx, "another", "another conversation"); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		session string
		depth   int
		action  string
	}{{"session", 0, ""}, {"another", 0, ""}, {"another", 1, ""}, {"session", 0, "compact"}} {
		prepared, err := prepareContext(ctx, workerproto.Command{SessionID: tc.session, Action: tc.action, Prompt: "continue"}, nil, tc.depth, 2)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(prepared.systemBase, change.Text) || !strings.Contains(prepared.systemBase, "global instructions take precedence") {
			t.Fatalf("preferences missing: %s", prepared.systemBase)
		}
		hasMemoryTool := false
		for _, schema := range prepared.schemas {
			hasMemoryTool = hasMemoryTool || schema["function"].(map[string]any)["name"] == "memory"
		}
		if hasMemoryTool != (tc.depth == 0) {
			t.Fatalf("memory tool depth %d: %v", tc.depth, hasMemoryTool)
		}
	}
	change.Action, change.PreviousText, change.Text = "update", change.Text, "Use imperial units."
	if err := db.ChangeMemory(ctx, change); err != nil {
		t.Fatal(err)
	}
	prepared, err := prepareContext(ctx, workerproto.Command{SessionID: "another", Prompt: "continue"}, nil, 0, 0)
	if err != nil || !strings.Contains(prepared.systemBase, change.Text) || strings.Contains(prepared.systemBase, change.PreviousText) {
		t.Fatalf("stale preferences: %s %v", prepared.systemBase, err)
	}
	change.Action, change.PreviousText, change.Text = "remove", change.Text, ""
	if err := db.ChangeMemory(ctx, change); err != nil {
		t.Fatal(err)
	}
	prepared, err = prepareContext(ctx, workerproto.Command{SessionID: "another", Prompt: "continue"}, nil, 0, 0)
	if err != nil || !strings.Contains(prepared.systemBase, "User's saved preferences at run start:\n[]") || strings.Contains(prepared.systemBase, change.PreviousText) {
		t.Fatalf("deleted preferences reloaded: %s %v", prepared.systemBase, err)
	}
}

func TestMemoryToolPersistsBeforeReportingSuccessAndRequiresCurrentText(t *testing.T) {
	db, ctx := checkpointFixture(t)
	if _, _, err := db.BeginRun(ctx, "session", "run", "user", "Remember my preferences"); err != nil {
		t.Fatal(err)
	}
	mutations := 0
	runtime := &toolRuntime{parent: workerproto.Command{RunID: "run", SessionID: "session"}, emit: func(result workerproto.Result) error {
		mutations++
		if result.Kind != "memory_change" || result.Memory == nil {
			t.Fatalf("invalid mutation: %+v", result)
		}
		_, err := db.ChangeRunMemory(ctx, "session", "run", *result.Memory)
		return err
	}}
	call := func(arguments string) (string, error) {
		return runtime.execute(ctx, scriptedToolCall("pref", "memory", arguments))
	}
	output, err := call(`{"action":"add","text":"回答简明。"}`)
	if err != nil || !strings.Contains(output, `"action":"add"`) {
		t.Fatalf("add: %s %v", output, err)
	}
	items, _ := db.Memories(ctx)
	if len(items) != 1 || mutations != 1 {
		t.Fatalf("add was not persisted: %+v", items)
	}
	output, err = call(`{"action":"list"}`)
	if err != nil || !strings.Contains(output, items[0].ID) || !strings.Contains(output, items[0].Text) {
		t.Fatalf("list: %s %v", output, err)
	}
	var listed struct{ MaxCount, MaxBytes, MaxTotalBytes int }
	if err := json.Unmarshal([]byte(output), &listed); err != nil || listed.MaxCount != store.MaxMemories || listed.MaxBytes != store.MaxMemoryBytes || listed.MaxTotalBytes != store.MaxMemoriesBytes {
		t.Fatalf("list omitted capacity limits: %s %v", output, err)
	}
	arguments, _ := json.Marshal(workerproto.MemoryChange{Action: "update", ID: items[0].ID, PreviousText: items[0].Text, Text: "回答简明，并提供依据。"})
	if _, err := call(string(arguments)); err != nil {
		t.Fatal(err)
	}
	if _, err := call(string(arguments)); !errors.Is(err, store.ErrMemoryChanged) {
		t.Fatalf("stale overwrite succeeded: %v", err)
	}
	before := mutations
	for _, arguments := range []string{`{"action":"update","id":"` + items[0].ID + `","text":"missing old text"}`, `{"action":"add","text":"x","unexpected":true}`, `{"action":"list","text":"invalid"}`} {
		if _, err := call(arguments); err == nil {
			t.Fatal("invalid input accepted")
		}
	}
	if mutations != before {
		t.Fatal("invalid call emitted a mutation")
	}
	runtime.depth = 1
	if _, err := call(`{"action":"add","text":"child preference"}`); err == nil || mutations != before {
		t.Fatal("child wrote preferences")
	}
	runtime.depth = 0
	runtime.emit = func(workerproto.Result) error { return errors.New("persistence failed") }
	if output, err := call(`{"action":"add","text":"not saved"}`); err == nil || output != "" {
		t.Fatal("failed persistence reported success")
	}
	if output, err := runtime.memory(context.Background(), provider.ToolCall{}); err == nil || output != "" {
		t.Fatal("empty call accepted")
	}
}

func TestMemoryToolAtomicUpdatesAndRemovals(t *testing.T) {
	db, ctx := checkpointFixture(t)
	if _, _, err := db.BeginRun(ctx, "session", "run", "user", "Remember my preferences"); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"Prefer short answers.", "Keep answers concise.", "Use metric units."} {
		if err := db.ChangeMemory(ctx, workerproto.MemoryChange{Action: "add", ID: uuid.NewString(), Text: text}); err != nil {
			t.Fatal(err)
		}
	}
	before, err := db.Memories(ctx)
	if err != nil {
		t.Fatal(err)
	}
	mutations := 0
	runtime := &toolRuntime{parent: workerproto.Command{RunID: "run", SessionID: "session"}, emit: func(result workerproto.Result) error {
		mutations++
		_, err := db.ChangeRunMemory(ctx, "session", "run", *result.Memory)
		return err
	}}
	change := workerproto.MemoryChange{Action: "update", Changes: []workerproto.MemoryEdit{
		{Action: "update", ID: before[0].ID, PreviousText: before[0].Text, Text: "  Answer concisely, with supporting evidence.  "},
		{Action: "remove", ID: before[1].ID, PreviousText: before[1].Text},
		{Action: "update", ID: before[2].ID, PreviousText: before[2].Text, Text: "Use SI units and Celsius."},
	}}
	arguments, err := json.Marshal(change)
	if err != nil {
		t.Fatal(err)
	}
	call := func(arguments string) (string, error) {
		return runtime.execute(ctx, scriptedToolCall("preferences", "memory", arguments))
	}
	output, err := call(string(arguments))
	var result workerproto.MemoryChange
	if err != nil || json.Unmarshal([]byte(output), &result) != nil || result.Action != "update" || len(result.Changes) != 3 || result.Changes[0].Text != strings.TrimSpace(change.Changes[0].Text) || result.Changes[1].Action != "remove" || mutations != 1 {
		t.Fatalf("batch result: %s mutations=%d err=%v", output, mutations, err)
	}
	after, err := db.Memories(ctx)
	if err != nil || len(after) != 2 || after[0].Text != result.Changes[0].Text || after[1].Text != result.Changes[2].Text {
		t.Fatalf("tool reported success before all edits persisted: %+v %v", after, err)
	}
	if output, err := call(string(arguments)); !errors.Is(err, store.ErrMemoryChanged) || output != "" {
		t.Fatalf("stale batch reported success: %s %v", output, err)
	}
	unchanged, err := db.Memories(ctx)
	if err != nil || !reflect.DeepEqual(unchanged, after) {
		t.Fatalf("rejected batch changed memories: %+v %v", unchanged, err)
	}
	beforeMutations := mutations
	for _, arguments := range []string{
		`{"action":"list","changes":[]}`,
		`{"action":"update","changes":[]}`,
		`{"action":"update","id":"` + before[0].ID + `","changes":[{"action":"remove","id":"` + before[1].ID + `","previousText":"old"}]}`,
		`{"action":"update","changes":[{"action":"remove","id":"` + before[0].ID + `","previousText":"old","unexpected":true}]}`,
		`{"action":"update","changes":[{"action":"remove","id":"` + before[0].ID + `","previousText":"old","changes":[]}]}`,
	} {
		if output, err := call(arguments); err == nil || output != "" {
			t.Fatalf("invalid batch accepted: %s %v", output, err)
		}
	}
	oversized := workerproto.MemoryChange{Action: "update"}
	for i := 0; i < 2; i++ {
		oversized.Changes = append(oversized.Changes, workerproto.MemoryEdit{Action: "update", ID: uuid.NewString(), PreviousText: strings.Repeat(string(rune(1)), store.MaxMemoryBytes), Text: strings.Repeat(string(rune(2)), store.MaxMemoryBytes)})
	}
	oversizedJSON, _ := json.Marshal(oversized)
	if _, err := call(string(oversizedJSON)); err == nil || !strings.Contains(err.Error(), "transfer size") {
		t.Fatalf("oversized batch reached IPC: %v", err)
	}
	runtime.depth = 1
	if _, err := call(string(arguments)); err == nil || mutations != beforeMutations {
		t.Fatal("invalid or child batch emitted a mutation")
	}
	runtime.depth = 0
	runtime.emit = func(workerproto.Result) error { return errors.New("commit unavailable") }
	if output, err := call(string(arguments)); err == nil || output != "" {
		t.Fatalf("failed commit reported batch success: %s %v", output, err)
	}
}
