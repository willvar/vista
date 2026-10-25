package server

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"vista/internal/workerproto"
)

func TestStateHubBoundsCoalescesAndCopiesRevisions(t *testing.T) {
	var hub stateHub
	wake, leave, ok := hub.subscribe()
	if !ok {
		t.Fatal("first subscription rejected")
	}
	defer leave()
	for range 50 {
		hub.notify("models")
	}
	<-wake
	select {
	case <-wake:
		t.Fatal("unbounded notifications")
	default:
	}
	point := hub.snapshot()
	if point.Epoch == "" || point.Revisions["models"] != 50 || len(point.Revisions) != len(stateTopics) {
		t.Fatalf("invalid checkpoint: %+v", point)
	}
	point.Revisions["models"] = 0
	if hub.snapshot().Revisions["models"] != 50 {
		t.Fatal("snapshot shares mutable revisions")
	}
	for range maxStateEventStreams - 1 {
		if _, _, ok := hub.subscribe(); !ok {
			t.Fatal("subscription rejected early")
		}
	}
	if _, _, ok := hub.subscribe(); ok {
		t.Fatal("stream limit ignored")
	}
	leave()
	leave()
	if hub.count() != maxStateEventStreams-1 {
		t.Fatal("double cleanup released another slot")
	}
	var next stateHub
	if next.snapshot().Epoch == point.Epoch {
		t.Fatal("a new server must have a new epoch")
	}
}

func TestStateStreamInitialCatchupAndDisconnect(t *testing.T) {
	consumer, boundary := newResultConsumerTest(t)
	app := &App{store: consumer.store}
	app.updates.notify("models")
	server := httptest.NewServer(http.HandlerFunc(app.stateEvents))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatal("not an SSE response")
	}
	reader := bufio.NewReader(response.Body)
	read := func() stateCheckpoint {
		t.Helper()
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				t.Fatal(err)
			}
			if strings.HasPrefix(line, "data: ") {
				var value stateCheckpoint
				if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &value); err != nil {
					t.Fatal(err)
				}
				return value
			}
		}
	}
	if point := read(); point.Revisions["models"] != 1 || point.Sessions != nil || point.Status != nil {
		t.Fatalf("missing initial revision: %+v", point)
	}
	app.updates.notify("sessions", "models")
	point := read()
	if point.Revisions["models"] != 2 || point.Revisions["sessions"] != 1 || point.Sessions == nil || point.Status != nil {
		t.Fatalf("missing change: %+v", point)
	}
	if len(point.Sessions.Items) != 1 || !point.Sessions.Items[0].Executing || point.Sessions.ThroughSequences["session"] != boundary {
		t.Fatalf("list and saved boundary disagree: %+v", point.Sessions)
	}
	finished, err := consumer.store.FinishRun(ctx, "session", "run", "answer", "succeeded", "Done")
	if err != nil {
		t.Fatal(err)
	}
	app.updates.notify("sessions", "status")
	point = read()
	if point.Sessions == nil || point.Sessions.Items[0].Executing || point.Sessions.Items[0].LatestReplySequence != finished.Sequence || point.Status == nil || point.Status["sessions"].(map[string]any)["running"] != float64(0) {
		t.Fatalf("completion payload is stale: %+v", point)
	}
	// Unchanged heartbeats and unrelated topics carry no repeated state payload.
	app.updates.notify("catalog")
	if point = read(); point.Sessions != nil || point.Status != nil || point.Revisions["catalog"] != 1 {
		t.Fatalf("unchanged data resent: %+v", point)
	}
	cancel()
	response.Body.Close()
	deadline := time.Now().Add(time.Second)
	for app.updates.count() != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if app.updates.count() != 0 {
		t.Fatal("disconnected client retained")
	}
}

func TestStateUpdateKeepsOversizedListsWithinFrameLimit(t *testing.T) {
	consumer, _ := newResultConsumerTest(t)
	for index := range 300 {
		if _, err := consumer.store.Create(consumer.ctx, fmt.Sprint(index), strings.Repeat("x", 200)); err != nil {
			t.Fatal(err)
		}
	}
	app := &App{store: consumer.store}
	before := app.updates.snapshot()
	app.updates.notify("sessions", "status")
	point, err := app.stateUpdate(consumer.ctx, "localhost", before)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(point)
	if err != nil || len(encoded) > maxStateSnapshotBytes || point.Sessions != nil || point.Status == nil || point.Revisions["sessions"] != 1 {
		t.Fatalf("oversized update cannot use HTTP recovery: bytes=%d point=%+v err=%v", len(encoded), point, err)
	}
}

func TestStateMutationPublishesAfterSuccess(t *testing.T) {
	for _, code := range []int{204, 400, 500} {
		app := &App{}
		committed := false
		handler := app.stateMutations(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if app.updates.snapshot().Revisions["sessions"] != 0 {
				t.Fatal("notified before commit")
			}
			committed = code < 400
			w.WriteHeader(code)
		}))
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPatch, "/api/sessions/example", nil))
		notified := app.updates.snapshot().Revisions["sessions"] > 0
		if notified != committed {
			t.Fatalf("code=%d notified=%v committed=%v", code, notified, committed)
		}
	}
}

func TestSessionMutationsRefreshOnlyAffectedViews(t *testing.T) {
	for _, tc := range []struct {
		name, method, path string
		sessions, status   uint64
	}{
		{"create", http.MethodPost, "/api/sessions", 1, 1},
		{"delete", http.MethodDelete, "/api/sessions/s", 1, 1},
		{"rename", http.MethodPatch, "/api/sessions/s", 1, 0},
		{"read", http.MethodPost, "/api/sessions/s/read", 1, 0},
		{"open", http.MethodPost, "/api/sessions/s/open", 1, 1},
		{"close", http.MethodPost, "/api/sessions/s/close", 1, 1},
		{"retry", http.MethodPost, "/api/sessions/s/retry", 1, 1},
		{"feedback", http.MethodPut, "/api/sessions/s/messages/m/feedback", 0, 0},
		{"stop request", http.MethodPost, "/api/sessions/s/jobs/j/stop", 0, 0},
		{"read snapshot", http.MethodGet, "/api/sessions/s/snapshot", 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app := &App{}
			handler := app.stateMutations(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }))
			handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(tc.method, tc.path, nil))
			point := app.updates.snapshot()
			if point.Revisions["sessions"] != tc.sessions || point.Revisions["status"] != tc.status || point.Revisions["models"] != 0 {
				t.Fatalf("unrelated or missing refresh: %+v", point)
			}
		})
	}
}

func TestSessionCommitNotificationsAreNotDuplicatedByMiddleware(t *testing.T) {
	for _, path := range []string{"/api/sessions/s/messages", "/api/sessions/s/compact", "/api/sessions/s/inputs/resume", "/api/sessions/s/selection"} {
		app := &App{}
		handler := app.stateMutations(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			app.notifySession("s")
			w.WriteHeader(202)
		}))
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, path, nil))
		point := app.updates.snapshot()
		if point.Revisions["sessions"] != 1 || point.Revisions["status"] != 1 {
			t.Fatalf("%s notified more than its commit: %+v", path, point)
		}
	}
}

func TestWorkerDetailsPersistWithoutWorkspaceReloads(t *testing.T) {
	consumer, before := newResultConsumerTest(t)
	app := &App{}
	consumer.changed = app.notifyWorkerState
	notified := 0
	consumer.notify = func() { notified++ }
	results := []workerproto.Result{
		{Kind: "model_request", RunID: "run", Turn: 1, ModelUse: &workerproto.ModelUse{Provider: "openai-chat", ID: "model"}},
		{Kind: "model_delta", RunID: "run", Turn: 1, Content: "Progress"},
		{Kind: "tool", RunID: "run", Status: "started", AckID: "tool", Tool: &workerproto.ToolProgress{CallID: "call", Name: "shell", Summary: "Read a file"}},
		{Kind: "tool", RunID: "run", Status: "succeeded", Content: "File contents", Tool: &workerproto.ToolProgress{CallID: "call", Name: "shell", Summary: "Read a file"}},
		{Kind: "model_result", RunID: "run", Turn: 1, ModelResult: &workerproto.ModelResult{Provider: "openai-chat", ID: "model", FinishReason: "stop"}},
	}
	for _, result := range results {
		if err := consumeTestResult(t, consumer, result); err != nil {
			t.Fatal(err)
		}
	}
	events, err := consumer.store.EventsAfter(consumer.ctx, consumer.sessionID, before, 100)
	if err != nil || len(events) != len(results) || notified != len(results) {
		t.Fatalf("conversation delivery lost: events=%d notifications=%d err=%v", len(events), notified, err)
	}
	point := app.updates.snapshot()
	if point.Revisions["sessions"] != 0 || point.Revisions["status"] != 0 {
		t.Fatalf("execution details invalidated workspace: %+v", point)
	}
	if err := consumeTestResult(t, consumer, workerproto.Result{RunID: "run", Status: "succeeded", Content: "Done"}); err != nil {
		t.Fatal(err)
	}
	point = app.updates.snapshot()
	if point.Revisions["sessions"] != 1 || point.Revisions["status"] != 1 || notified != len(results)+1 {
		t.Fatalf("completion did not synchronize workspace and conversation: %+v, notifications=%d", point, notified)
	}
}
