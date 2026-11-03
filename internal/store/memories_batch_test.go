package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"vista/internal/workerproto"
)

func seedMemories(t *testing.T, db *Store, ctx context.Context, texts ...string) []Memory {
	t.Helper()
	for _, text := range texts {
		if err := db.ChangeMemory(ctx, workerproto.MemoryChange{Action: "add", ID: uuid.NewString(), Text: text}); err != nil {
			t.Fatal(err)
		}
	}
	items, err := db.Memories(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return items
}

func TestMemoryBatchConsolidatesAndUpdatesWithOneAuditEvent(t *testing.T) {
	db, ctx := executionStore(t)
	before := seedMemories(t, db, ctx, "Prefer concise answers.", "Use concise answers.", "Use metric units.", "Keep code comments in English.")
	change := workerproto.MemoryChange{Action: "update", Changes: []workerproto.MemoryEdit{
		{Action: "update", ID: before[0].ID, PreviousText: before[0].Text, Text: "  " + before[1].Text + "  "},
		{Action: "remove", ID: before[1].ID, PreviousText: before[1].Text},
		{Action: "update", ID: before[2].ID, PreviousText: before[2].Text, Text: "Use SI units, including Celsius."},
	}}
	event, err := db.ChangeRunMemory(ctx, "session", "run", change)
	if err != nil || event == nil || event.Kind != "memory_changed" {
		t.Fatalf("batch: %+v %v", event, err)
	}
	after, err := db.Memories(ctx)
	if err != nil || len(after) != 3 || after[0].ID != before[0].ID || after[0].Text != before[1].Text || after[0].CreatedAt != before[0].CreatedAt || after[1].ID != before[2].ID || after[1].Text != change.Changes[2].Text || after[1].CreatedAt != before[2].CreatedAt || after[2] != before[3] {
		t.Fatalf("consolidation changed identities or unrelated data: %+v %v", after, err)
	}
	if after[0].UpdatedAt != after[1].UpdatedAt {
		t.Fatal("batch did not share one update timestamp")
	}
	var audit struct {
		RunID  string                   `json:"runId"`
		Change workerproto.MemoryChange `json:"change"`
	}
	if err := json.Unmarshal(event.Data, &audit); err != nil || audit.RunID != "run" || len(audit.Change.Changes) != 3 || audit.Change.Changes[0].Text != before[1].Text || audit.Change.Changes[1].Action != "remove" {
		t.Fatalf("incomplete audit: %s %v", event.Data, err)
	}
	if change.Changes[0].Text != "  "+before[1].Text+"  " {
		t.Fatal("store mutated its caller's batch")
	}
	events, err := db.EventsAfter(ctx, "session", event.Sequence-1, 100)
	if err != nil || len(events) != 1 || events[0].Sequence != event.Sequence {
		t.Fatalf("batch did not produce exactly one event: %+v %v", events, err)
	}
}

func TestMemoryBatchUsesFinalBytesAndCanSwapTexts(t *testing.T) {
	db, ctx := executionStore(t)
	texts := []string{strings.Repeat("a", MaxMemoryBytes/2), strings.Repeat("b", MaxMemoryBytes/2)}
	for i := 0; i < 7; i++ {
		texts = append(texts, strings.Repeat(string(rune('c'+i)), MaxMemoryBytes))
	}
	before := seedMemories(t, db, ctx, texts...)
	merged := strings.Repeat("z", MaxMemoryBytes)
	change := workerproto.MemoryChange{Action: "update", Changes: []workerproto.MemoryEdit{
		{Action: "update", ID: before[0].ID, PreviousText: before[0].Text, Text: merged},
		{Action: "remove", ID: before[1].ID, PreviousText: before[1].Text},
		{Action: "update", ID: before[2].ID, PreviousText: before[2].Text, Text: before[3].Text},
		{Action: "update", ID: before[3].ID, PreviousText: before[3].Text, Text: before[2].Text},
	}}
	if _, err := db.ChangeRunMemory(ctx, "session", "run", change); err != nil {
		t.Fatalf("final state fits despite temporary growth and duplicate texts: %v", err)
	}
	after, err := db.Memories(ctx)
	if err != nil || len(after) != 8 || after[0].Text != merged || after[1].Text != before[3].Text || after[2].Text != before[2].Text {
		t.Fatalf("incorrect final values: %+v %v", after, err)
	}
	bytes := 0
	for _, item := range after {
		bytes += len(item.Text)
	}
	if bytes != MaxMemoriesBytes {
		t.Fatalf("expected exactly full memory: %d", bytes)
	}
	if err := db.ChangeMemory(ctx, workerproto.MemoryChange{Action: "add", ID: uuid.NewString(), Text: "one byte too many"}); !errors.Is(err, ErrMemoryLimit) {
		t.Fatalf("capacity guard disappeared: %v", err)
	}
}

func TestMemoryBatchFreesCountAtCapacity(t *testing.T) {
	db, ctx := executionStore(t)
	texts := make([]string, MaxMemories)
	for i := range texts {
		texts[i] = fmt.Sprintf("Preference %d.", i)
	}
	before := seedMemories(t, db, ctx, texts...)
	addition := workerproto.MemoryChange{Action: "add", ID: uuid.NewString(), Text: "A new lasting preference."}
	if err := db.ChangeMemory(ctx, addition); !errors.Is(err, ErrMemoryLimit) {
		t.Fatalf("expected count limit: %v", err)
	}
	if err := db.ChangeMemory(ctx, workerproto.MemoryChange{Action: "update", Changes: []workerproto.MemoryEdit{
		{Action: "update", ID: before[0].ID, PreviousText: before[0].Text, Text: before[0].Text + " " + before[1].Text},
		{Action: "remove", ID: before[1].ID, PreviousText: before[1].Text},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := db.ChangeMemory(ctx, addition); err != nil {
		t.Fatalf("consolidation did not make space: %v", err)
	}
}

func TestMemoryBatchFailurePreservesAllMemoriesAndEvents(t *testing.T) {
	for _, scenario := range []string{"stale update", "stale removal", "missing target", "capacity", "duplicate existing", "duplicate replacements", "write failure", "audit failure", "inactive run"} {
		t.Run(scenario, func(t *testing.T) {
			db, ctx := executionStore(t)
			texts := []string{"First preference.", "Second preference.", "Unrelated preference."}
			for i := 0; i < 7; i++ {
				texts = append(texts, strings.Repeat(string(rune('a'+i)), MaxMemoryBytes))
			}
			before := seedMemories(t, db, ctx, texts...)
			change := workerproto.MemoryChange{Action: "update", Changes: []workerproto.MemoryEdit{
				{Action: "update", ID: before[0].ID, PreviousText: before[0].Text, Text: "Combined preferences."},
				{Action: "remove", ID: before[1].ID, PreviousText: before[1].Text},
				{Action: "update", ID: before[2].ID, PreviousText: before[2].Text, Text: "Updated third preference."},
			}}
			var want error
			switch scenario {
			case "stale update":
				change.Changes[2].PreviousText = "Outdated third preference."
				want = ErrMemoryChanged
			case "stale removal":
				change.Changes[1].PreviousText = "Outdated second preference."
				want = ErrMemoryChanged
			case "missing target":
				change.Changes[1].ID = uuid.NewString()
				want = ErrMemoryChanged
			case "capacity":
				change.Changes[0].Text = strings.Repeat("z", MaxMemoryBytes)
				want = ErrMemoryLimit
			case "duplicate existing":
				change.Changes[0].Text = before[3].Text
				want = ErrMemoryExists
			case "duplicate replacements":
				change.Changes[2].Text = change.Changes[0].Text
				want = ErrMemoryExists
			case "write failure":
				if _, err := db.db.ExecContext(ctx, `CREATE TRIGGER reject_memory BEFORE INSERT ON memories WHEN NEW.id='`+before[2].ID+`' BEGIN SELECT RAISE(ABORT,'injected memory write failure'); END`); err != nil {
					t.Fatal(err)
				}
			case "audit failure":
				if _, err := db.db.ExecContext(ctx, `CREATE TRIGGER reject_memory_audit BEFORE INSERT ON events WHEN NEW.kind='memory_changed' BEGIN SELECT RAISE(ABORT,'injected memory audit failure'); END`); err != nil {
					t.Fatal(err)
				}
			case "inactive run":
				if _, err := db.FinishRun(ctx, "session", "run", "answer", "succeeded", "done"); err != nil {
					t.Fatal(err)
				}
			}
			beforeEvents, err := db.EventsAfter(ctx, "session", 0, 100)
			if err != nil {
				t.Fatal(err)
			}
			event, err := db.ChangeRunMemory(ctx, "session", "run", change)
			if err == nil || event != nil || want != nil && !errors.Is(err, want) {
				t.Fatalf("expected rejected batch (%v): %+v %v", want, event, err)
			}
			after, err := db.Memories(ctx)
			if err != nil || !reflect.DeepEqual(after, before) {
				t.Fatalf("failed batch changed memories: %+v %v", after, err)
			}
			afterEvents, err := db.EventsAfter(ctx, "session", 0, 100)
			if err != nil || !reflect.DeepEqual(afterEvents, beforeEvents) {
				t.Fatalf("failed batch changed audit: %+v %v", afterEvents, err)
			}
		})
	}
}

func TestMemoryBatchConflictsWithConcurrentSingleEdit(t *testing.T) {
	db, ctx := executionStore(t)
	before := seedMemories(t, db, ctx, "First preference.", "Second preference.")
	batch := workerproto.MemoryChange{Action: "update", Changes: []workerproto.MemoryEdit{
		{Action: "update", ID: before[0].ID, PreviousText: before[0].Text, Text: "Combined preferences."},
		{Action: "remove", ID: before[1].ID, PreviousText: before[1].Text},
	}}
	single := workerproto.MemoryChange{Action: "update", ID: before[1].ID, PreviousText: before[1].Text, Text: "Edited in settings."}
	var batchErr, singleErr error
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _, batchErr = db.ChangeRunMemory(ctx, "session", "run", batch) }()
	go func() { defer wg.Done(); singleErr = db.ChangeMemory(ctx, single) }()
	wg.Wait()
	after, err := db.Memories(ctx)
	if err != nil {
		t.Fatal(err)
	}
	switch {
	case batchErr == nil && errors.Is(singleErr, ErrMemoryChanged):
		if len(after) != 1 || after[0].ID != before[0].ID || after[0].Text != batch.Changes[0].Text {
			t.Fatalf("partial consolidation: %+v", after)
		}
	case singleErr == nil && errors.Is(batchErr, ErrMemoryChanged):
		if len(after) != 2 || after[0] != before[0] || after[1].Text != single.Text {
			t.Fatalf("batch overwrote concurrent change: %+v", after)
		}
	default:
		t.Fatalf("expected one success and one conflict: batch=%v single=%v", batchErr, singleErr)
	}
}

func TestMemoryBatchCanConsolidateTheFullByteBudget(t *testing.T) {
	db, ctx := executionStore(t)
	texts := make([]string, MaxMemoriesBytes/MaxMemoryBytes)
	for i := range texts {
		texts[i] = strings.Repeat(string(rune('a'+i)), MaxMemoryBytes)
	}
	before := seedMemories(t, db, ctx, texts...)
	change := workerproto.MemoryChange{Action: "update"}
	for i, item := range before {
		edit := workerproto.MemoryEdit{Action: "remove", ID: item.ID, PreviousText: item.Text}
		if i == 0 {
			edit.Action, edit.Text = "update", strings.Repeat("z", MaxMemoryBytes)
		}
		change.Changes = append(change.Changes, edit)
	}
	event, err := db.ChangeRunMemory(ctx, "session", "run", change)
	if err != nil || event == nil || len(event.Data) <= 32<<10 || len(event.Data) >= workerproto.MaxResultFrame {
		t.Fatalf("bounded batch audit did not fit: %+v %v", event, err)
	}
	after, err := db.Memories(ctx)
	if err != nil || len(after) != 1 || after[0].ID != before[0].ID || after[0].Text != change.Changes[0].Text {
		t.Fatalf("full-budget consolidation: %+v %v", after, err)
	}
}

func TestMemoryBatchRejectsAmbiguousAndOversizedChanges(t *testing.T) {
	id := uuid.NewString()
	edit := workerproto.MemoryEdit{Action: "update", ID: id, Text: "New preference.", PreviousText: "Old preference."}
	for name, change := range map[string]workerproto.MemoryChange{
		"empty":             {Action: "update", Changes: []workerproto.MemoryEdit{}},
		"mixed fields":      {Action: "update", ID: id, Changes: []workerproto.MemoryEdit{edit}},
		"mixed text":        {Action: "update", Text: "ambiguous", Changes: []workerproto.MemoryEdit{edit}},
		"mixed previous":    {Action: "update", PreviousText: "ambiguous", Changes: []workerproto.MemoryEdit{edit}},
		"remove container":  {Action: "remove", Changes: []workerproto.MemoryEdit{edit}},
		"add container":     {Action: "add", ID: id, Text: "new", Changes: []workerproto.MemoryEdit{edit}},
		"duplicate target":  {Action: "update", Changes: []workerproto.MemoryEdit{edit, edit}},
		"missing previous":  {Action: "update", Changes: []workerproto.MemoryEdit{{Action: "update", ID: id, Text: "new"}}},
		"invalid id":        {Action: "update", Changes: []workerproto.MemoryEdit{{Action: "remove", ID: "invalid", PreviousText: "old"}}},
		"blank replacement": {Action: "update", Changes: []workerproto.MemoryEdit{{Action: "update", ID: id, PreviousText: "old", Text: "  "}}},
		"remove with text":  {Action: "update", Changes: []workerproto.MemoryEdit{{Action: "remove", ID: id, PreviousText: "old", Text: "not a replacement"}}},
		"add item":          {Action: "update", Changes: []workerproto.MemoryEdit{{Action: "add", ID: id, Text: "new"}}},
		"too many targets":  {Action: "update", Changes: make([]workerproto.MemoryEdit, MaxMemories+1)},
	} {
		t.Run(name, func(t *testing.T) {
			if err := ValidateMemoryChange(change); !errors.Is(err, ErrInvalidMemory) {
				t.Fatalf("invalid batch accepted: %v", err)
			}
		})
	}
	oversized := workerproto.MemoryChange{Action: "update"}
	for i := 0; i < 2; i++ {
		oversized.Changes = append(oversized.Changes, workerproto.MemoryEdit{Action: "update", ID: uuid.NewString(), PreviousText: strings.Repeat(string(rune(1)), MaxMemoryBytes), Text: strings.Repeat(string(rune(2)), MaxMemoryBytes)})
	}
	if err := ValidateMemoryChange(oversized); !errors.Is(err, ErrInvalidMemory) || !strings.Contains(err.Error(), "transfer size") {
		t.Fatalf("JSON escaping was not included in the IPC budget: %v", err)
	}
}
