package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"vista/internal/store"
	"vista/internal/workerproto"
)

func TestAgentSessionActionConfirmation(t *testing.T) {
	a, source := inputQueueFixture(t)
	sink := &workerCommandSink{commands: make(chan string, 8)}
	w := &worker{input: sink}
	a.workers[source] = w
	create := func(title string) string {
		t.Helper()
		id := uuid.NewString()
		if _, err := a.store.Create(a.ctx, id, title); err != nil {
			t.Fatal(err)
		}
		return id
	}
	idle, changed, unchecked, active := create("idle"), create("changed"), create("unchecked"), create("active")
	if _, _, err := a.store.BeginRun(a.ctx, active, "other", "other-user", "private work"); err != nil {
		t.Fatal(err)
	}
	prepare := func(kind string, ids ...string) string {
		t.Helper()
		id := uuid.NewString()
		if err := a.requestSessionAction(source, w, workerproto.Result{RunID: "run", AckID: id, SessionAction: &workerproto.SessionAction{Kind: kind, SessionIDs: ids}}); err != nil {
			t.Fatal(err)
		}
		return id
	}
	confirm := func(requestID string, ids ...string) *httptest.ResponseRecorder {
		t.Helper()
		body, _ := json.Marshal(map[string]any{"sessionIds": append([]string{}, ids...)})
		rec := httptest.NewRecorder()
		a.session(rec, httptest.NewRequest(http.MethodPost, "/api/sessions/"+source+"/session-action/"+requestID, strings.NewReader(string(body))))
		return rec
	}
	assertOpen := func(id string, open bool) {
		t.Helper()
		if target, err := a.store.Get(a.ctx, id); err != nil || target.Open != open {
			t.Fatalf("expected open=%v: %+v %v", open, target, err)
		}
	}
	ack := func() workerproto.Command {
		t.Helper()
		var cmd workerproto.Command
		if err := json.Unmarshal([]byte(awaitWorkerTest(t, sink.commands)), &cmd); err != nil {
			t.Fatal(err)
		}
		return cmd
	}

	id := prepare("close", idle, changed, unchecked, active)
	pending := a.sessionAction(source)
	if pending == nil || len(pending.Targets) != 3 || pending.Targets[0].Title != "idle" {
		t.Fatalf("invalid authoritative preview: %+v", pending)
	}
	assertOpen(idle, true)
	if got := confirm(id, idle, active); got.Code != 400 {
		t.Fatalf("unpreviewed target: %d %s", got.Code, got.Body)
	}
	if err := a.store.Rename(a.ctx, changed, "edited after preview"); err != nil {
		t.Fatal(err)
	}
	response := confirm(id, idle, changed)
	if response.Code != 200 {
		t.Fatalf("confirm: %d %s", response.Code, response.Body)
	}
	result := ack().SessionActionResult
	if result == nil || len(result.Items) != 4 || result.Items[0].Status != "closed" || result.Items[1].Reason != "changed_since_preview" || result.Items[2].Reason != "not_confirmed" || result.Items[3].Reason != "active_work" {
		t.Fatalf("close results: %+v", result)
	}
	assertOpen(idle, false)
	assertOpen(changed, true)
	assertOpen(unchecked, true)
	assertOpen(active, true)
	if got := confirm(id, idle); got.Code != 409 {
		t.Fatalf("replayed approval: %d", got.Code)
	}

	id = prepare("delete", idle, unchecked)
	if err := a.store.SetOpen(a.ctx, idle, true); err != nil {
		t.Fatal(err)
	}
	if err := a.store.SetOpen(a.ctx, idle, false); err != nil {
		t.Fatal(err)
	}
	if got := confirm(id, idle); got.Code != 200 {
		t.Fatalf("delete conflict: %d %s", got.Code, got.Body)
	}
	result = ack().SessionActionResult
	if result.Items[0].Reason != "changed_since_preview" || result.Items[1].Reason != "must_be_closed" {
		t.Fatalf("delete conflict: %+v", result)
	}
	assertOpen(idle, false)
	id = prepare("delete", idle)
	if got := confirm(id, idle); got.Code != 200 || ack().SessionActionResult.Items[0].Status != "deleted" {
		t.Fatalf("delete: %d %s", got.Code, got.Body)
	}
	if _, err := a.store.Get(a.ctx, idle); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("deleted session remains: %v", err)
	}

	for _, result := range []workerproto.Result{
		{Depth: 1, SessionAction: &workerproto.SessionAction{Kind: "close", SessionIDs: []string{unchecked}}},
		{SessionAction: &workerproto.SessionAction{Kind: "close", SessionIDs: []string{source}}},
	} {
		result.RunID, result.AckID = "run", uuid.NewString()
		if err := a.requestSessionAction(source, w, result); err != nil || ack().Error == "" || a.sessionAction(source) != nil {
			t.Fatalf("invalid caller or target was accepted: %+v %v", result, err)
		}
	}
	id = prepare("close", unchecked)
	if err := a.store.RequestRunCancellation(a.ctx, source, "run"); err != nil {
		t.Fatal(err)
	}
	if got := confirm(id, unchecked); got.Code != 200 || ack().SessionActionResult.Items[0].Reason != "source_run_ended" {
		t.Fatalf("canceled source: %d %s", got.Code, got.Body)
	}
	assertOpen(unchecked, true)
}
