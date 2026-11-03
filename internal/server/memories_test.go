package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"
	"vista/internal/store"
	"vista/internal/workerproto"
)

func TestMemorySettingsCRUDAndConflict(t *testing.T) {
	consumer, _ := newResultConsumerTest(t)
	app := &App{store: consumer.store}
	request := func(method, body string, status int, code string) []store.Memory {
		t.Helper()
		w := httptest.NewRecorder()
		app.memories(w, httptest.NewRequest(method, "/api/memories", strings.NewReader(body)))
		if w.Code != status || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%s %s: %d %s", method, body, w.Code, w.Body.String())
		}
		if code != "" {
			var result struct{ Code string }
			if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || result.Code != code {
				t.Fatalf("error response: %s %v", w.Body.String(), err)
			}
			return nil
		}
		var result struct {
			Memories                          []store.Memory
			MaxBytes, MaxCount, MaxTotalBytes int
		}
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || result.Memories == nil || result.MaxBytes != store.MaxMemoryBytes {
			t.Fatalf("snapshot: %s %v", w.Body.String(), err)
		}
		return result.Memories
	}
	if items := request("GET", "", 200, ""); len(items) != 0 {
		t.Fatal(items)
	}
	items := request("POST", `{"action":"add","text":"回答简明。"}`, 200, "")
	if len(items) != 1 {
		t.Fatal(items)
	}
	id := items[0].ID
	request("POST", `{"action":"add","text":"回答简明。"}`, 409, "memory_exists")
	request("POST", `{"action":"update","id":"`+id+`","previousText":"回答简明。","text":"提供依据。"}`, 200, "")
	request("POST", `{"action":"update","id":"`+id+`","previousText":"回答简明。","text":"stale"}`, 409, "memory_changed")
	request("POST", `{"action":"remove","id":"`+id+`","previousText":"回答简明。"}`, 409, "memory_changed")
	request("POST", `{"action":"remove","id":"`+id+`","previousText":"提供依据。"}`, 200, "")
	if items := request("GET", "", 200, ""); len(items) != 0 {
		t.Fatal(items)
	}
	for _, body := range []string{`null`, `{}`, `{"action":"list"}`, `{"action":"add","id":"forged","text":"x"}`, `{"action":"add","text":"x","extra":true}`, `{"action":"add","text":"x"} {}`, `{"action":"update","id":"` + id + `","text":"missing previous"}`, `{"action":"add","text":"` + strings.Repeat("x", store.MaxMemoryBytes+1) + `"}`} {
		request("POST", body, 400, "memory_invalid")
	}
	w := httptest.NewRecorder()
	app.memories(w, httptest.NewRequest("DELETE", "/api/memories", nil))
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatal(w.Code)
	}
}

func TestMemorySettingsAtomicUpdates(t *testing.T) {
	consumer, _ := newResultConsumerTest(t)
	app := &App{store: consumer.store}
	first, second := uuid.NewString(), uuid.NewString()
	for _, change := range []workerproto.MemoryChange{
		{Action: "add", ID: first, Text: "Use concise answers."},
		{Action: "add", ID: second, Text: "Prefer short replies."},
	} {
		if err := app.store.ChangeMemory(consumer.ctx, change); err != nil {
			t.Fatal(err)
		}
	}
	change := workerproto.MemoryChange{Action: "update", Changes: []workerproto.MemoryEdit{
		{Action: "update", ID: first, PreviousText: "Use concise answers.", Text: "Be concise and provide evidence."},
		{Action: "remove", ID: second, PreviousText: "Prefer short replies."},
	}}
	body, _ := json.Marshal(change)
	w := httptest.NewRecorder()
	app.memories(w, httptest.NewRequest(http.MethodPost, "/api/memories", strings.NewReader(string(body))))
	var snapshot memorySnapshot
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &snapshot) != nil || len(snapshot.Memories) != 1 || snapshot.Memories[0].ID != first || snapshot.Memories[0].Text != change.Changes[0].Text || app.updates.snapshot().Revisions["memories"] != 1 {
		t.Fatalf("batch response or notification: %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	app.memories(w, httptest.NewRequest(http.MethodPost, "/api/memories", strings.NewReader(string(body))))
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "memory_changed") || app.updates.snapshot().Revisions["memories"] != 1 {
		t.Fatalf("stale HTTP batch published a change: %d %s", w.Code, w.Body.String())
	}
}

func TestMemoryWorkerBatchACKAndSnapshotFollowFullCommit(t *testing.T) {
	consumer, sequence := newResultConsumerTest(t)
	app := &App{store: consumer.store}
	consumer.changed = app.notifyWorkerState
	for i := 0; i < store.MaxMemoriesBytes/store.MaxMemoryBytes; i++ {
		if err := app.store.ChangeMemory(consumer.ctx, workerproto.MemoryChange{Action: "add", ID: uuid.NewString(), Text: strings.Repeat(string(rune('a'+i)), store.MaxMemoryBytes)}); err != nil {
			t.Fatal(err)
		}
	}
	before, err := app.store.Memories(consumer.ctx)
	if err != nil {
		t.Fatal(err)
	}
	change := workerproto.MemoryChange{Action: "update"}
	for i, item := range before[:len(before)-1] {
		edit := workerproto.MemoryEdit{Action: "remove", ID: item.ID, PreviousText: item.Text}
		if i < 2 {
			edit.Action, edit.Text = "update", strings.Repeat(string(rune('x'+i)), store.MaxMemoryBytes)
		}
		change.Changes = append(change.Changes, edit)
	}
	notifications := 0
	consumer.notify = func() { notifications++ }
	var acks []workerproto.Command
	consumer.send = func(command workerproto.Command) error {
		acks = append(acks, command)
		if command.Error != "" {
			return nil
		}
		items, err := app.store.Memories(consumer.ctx)
		if err != nil || len(items) != 3 || items[0].Text != change.Changes[0].Text || items[1].Text != change.Changes[1].Text || items[2] != before[len(before)-1] || notifications != 1 || app.updates.snapshot().Revisions["memories"] != 1 {
			t.Fatalf("ACK preceded full persistence/publication: %+v %v", items, err)
		}
		return nil
	}
	result := workerproto.Result{RunID: "run", Kind: "memory_change", AckID: "memory-batch", Memory: &change}
	frame, err := json.Marshal(result)
	if err != nil || len(frame) > workerproto.MaxResultFrame {
		t.Fatalf("batch exceeded Worker frame: %d %v", len(frame), err)
	}
	if err := consumer.consumeFrame(frame); err != nil {
		t.Fatal(err)
	}
	if len(acks) != 1 || acks[0].Error != "" {
		t.Fatalf("batch was not acknowledged: %+v", acks)
	}
	point, err := app.stateUpdate(consumer.ctx, "localhost", stateCheckpoint{Revisions: map[string]uint64{}})
	if err != nil || point.Memories == nil || point.Memories.Total != 3 || point.Sessions != nil || point.Status != nil {
		t.Fatalf("batch pushed incomplete or unrelated state: %+v %v", point, err)
	}
	committed, err := app.store.Memories(consumer.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := consumer.consumeFrame(frame); err != nil {
		t.Fatalf("stale batch terminated the Worker protocol: %v", err)
	}
	if len(acks) != 2 || acks[1].Error == "" || notifications != 1 || app.updates.snapshot().Revisions["memories"] != 1 {
		t.Fatalf("rejected batch notified or acknowledged success: %+v notifications=%d", acks, notifications)
	}
	unchanged, err := app.store.Memories(consumer.ctx)
	if err != nil || !reflect.DeepEqual(unchanged, committed) {
		t.Fatalf("stale batch changed synchronized state: %+v %v", unchanged, err)
	}
	events, err := app.store.EventsAfter(consumer.ctx, consumer.sessionID, sequence, 100)
	if err != nil || len(events) != 1 || events[0].Kind != "memory_changed" {
		t.Fatalf("batch audit was not atomic: %+v %v", events, err)
	}
}

func TestMemoryWorkerACKFollowsCommitAndReportsConflicts(t *testing.T) {
	consumer, _ := newResultConsumerTest(t)
	change := workerproto.MemoryChange{Action: "add", ID: uuid.NewString(), Text: "Keep answers concise."}
	var acks []workerproto.Command
	notifications := 0
	consumer.notify = func() { notifications++ }
	consumer.send = func(command workerproto.Command) error {
		acks = append(acks, command)
		if command.Error == "" {
			items, err := consumer.store.Memories(consumer.ctx)
			if err != nil || len(items) != 1 || items[0].Text != change.Text {
				t.Fatalf("ACK preceded persistence: %+v %v", items, err)
			}
		}
		return nil
	}
	result := workerproto.Result{RunID: "run", Kind: "memory_change", AckID: "memory-ack", Memory: &change}
	if err := consumeTestResult(t, consumer, result); err != nil {
		t.Fatal(err)
	}
	if len(acks) != 1 || acks[0].Error != "" || notifications != 1 {
		t.Fatalf("ACK: %+v, notifications %d", acks, notifications)
	}
	change.Action, change.PreviousText, change.Text = "update", "stale", "must not overwrite"
	if err := consumeTestResult(t, consumer, result); err != nil {
		t.Fatalf("conflict killed Worker: %v", err)
	}
	if len(acks) != 2 || acks[1].Error == "" || notifications != 1 {
		t.Fatalf("conflict acknowledged success: %+v", acks)
	}
	result.Depth, result.TaskID = 1, "child"
	if err := consumeTestResult(t, consumer, result); err == nil || len(acks) != 2 {
		t.Fatal("child mutation accepted")
	}
	result.Depth, result.TaskID = 0, "main"
	if _, err := consumer.store.FinishRun(consumer.ctx, "session", "run", "answer", "succeeded", "done"); err != nil {
		t.Fatal(err)
	}
	if err := consumeTestResult(t, consumer, result); err != nil {
		t.Fatal(err)
	}
	if len(acks) != 3 || acks[2].Error == "" {
		t.Fatal("inactive run accepted")
	}
}
