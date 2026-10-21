package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
