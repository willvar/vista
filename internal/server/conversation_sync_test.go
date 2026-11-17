package server

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"vista/internal/store"
	"vista/internal/workerproto"
)

func TestSnapshotAndBoundedReplayAPI(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "vista.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	id, run := uuid.NewString(), uuid.NewString()
	st.Create(ctx, id, "Snapshot")
	st.BeginRun(ctx, id, run, "u", "question")
	result, err := st.AppendRunEvent(ctx, id, run, "model_result", workerproto.Result{RunID: run, Turn: 1, ModelResult: &workerproto.ModelResult{Provider: "openai-chat", ID: "model", ReasoningEffort: "high", FinishReason: "stop"}})
	if err != nil {
		t.Fatal(err)
	}
	finished, err := st.FinishRun(ctx, id, run, "a", "succeeded", "answer")
	if err != nil {
		t.Fatal(err)
	}
	app := &App{store: st}
	w := httptest.NewRecorder()
	app.session(w, httptest.NewRequest(http.MethodGet, "/api/sessions/"+id+"/snapshot", nil))
	var view struct {
		store.SyncCheckpoint
		Messages []messageWithResults `json:"messages"`
		Events   []store.Event        `json:"events"`
	}
	if err = json.Unmarshal(w.Body.Bytes(), &view); err != nil {
		t.Fatal(err, w.Body.String())
	}
	if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" || len(view.Messages) != 2 || len(view.Messages[1].ModelResults) != 1 || view.Messages[1].ModelResults[0].ReasoningEffort != "high" || view.ThroughSequence != finished.Sequence {
		t.Fatalf("bad core snapshot: %d %s", w.Code, w.Body.String())
	}
	for _, query := range []struct {
		query  string
		status int
	}{
		{fmt.Sprintf("after=0&through=%d", result.Sequence), 200},
		{"after=2&through=1", 400}, {"after=0&through=bad", 400}, {"after=0&through=0", 400},
	} {
		w := httptest.NewRecorder()
		app.session(w, httptest.NewRequest(http.MethodGet, "/api/sessions/"+id+"/event-page?"+query.query, nil))
		if w.Code != query.status {
			t.Fatalf("%s: %d", query.query, w.Code)
		}
		if w.Code == 200 {
			var events []store.Event
			if err := json.Unmarshal(w.Body.Bytes(), &events); err != nil {
				t.Fatal(err)
			}
			if len(events) != 2 || events[len(events)-1].Sequence != result.Sequence {
				t.Fatalf("replay escaped boundary: %+v", events)
			}
		}
	}
}

func TestSummaryReferencesPreserveLiveReplayAndCanonicalOutput(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	st, err := store.Open(filepath.Join(t.TempDir(), "summary.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	id, run := uuid.NewString(), uuid.NewString()
	if _, err := st.Create(ctx, id, "Summary"); err != nil {
		t.Fatal(err)
	}
	_, initial, err := st.BeginRun(ctx, id, run, "input", "question")
	if err != nil {
		t.Fatal(err)
	}
	app := &App{store: st, ctx: ctx}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { app.eventStream(w, r, id) }))
	defer server.Close()
	client := &http.Client{Timeout: 5 * time.Second}
	open := func() *http.Response {
		t.Helper()
		response, err := client.Get(fmt.Sprintf("%s?after=%d", server.URL, initial.Sequence))
		if err != nil {
			t.Fatal(err)
		}
		return response
	}
	type summaryChunk struct {
		RunID        string  `json:"runId"`
		Purpose      string  `json:"purpose"`
		Turn         int32   `json:"turn"`
		Offset       int     `json:"offset"`
		ContentBytes int     `json:"contentBytes"`
		Content      *string `json:"content"`
	}
	checkChunk := func(event store.Event, offset int) int {
		t.Helper()
		var chunk summaryChunk
		if err := json.Unmarshal(event.Data, &chunk); err != nil || chunk.RunID != run || chunk.Purpose != "summary" || chunk.Turn != 3 || chunk.Offset != offset || chunk.ContentBytes != 2048 || chunk.Content != nil {
			t.Fatalf("invalid summary reference at %d: %s (%v)", event.Sequence, event.Data, err)
		}
		return chunk.ContentBytes
	}
	readPoint := func(scanner *bufio.Scanner, cursor *int64) (store.SyncCheckpoint, int, int, int) {
		t.Helper()
		name, chunks, bytes, wireBytes := "", 0, 0, 0
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "event: ") {
				name = strings.TrimPrefix(line, "event: ")
			}
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			data := []byte(strings.TrimPrefix(line, "data: "))
			if name == "record" {
				var event struct {
					store.Event
					AfterSequence int64 `json:"afterSequence"`
				}
				if err := json.Unmarshal(data, &event); err != nil || event.AfterSequence != *cursor || event.Sequence <= *cursor {
					t.Fatalf("record coverage lost: %s (%v)", data, err)
				}
				*cursor = event.Sequence
				if event.Kind == "model_delta" {
					bytes += checkChunk(event.Event, bytes)
					chunks++
					wireBytes += len(data)
				}
			} else if name == "sync" {
				var point store.SyncCheckpoint
				if err := json.Unmarshal(data, &point); err != nil || point.ThroughSequence != *cursor {
					t.Fatalf("checkpoint coverage lost: %s (%v)", data, err)
				}
				return point, chunks, bytes, wireBytes
			}
		}
		t.Fatalf("missing checkpoint: %v", scanner.Err())
		return store.SyncCheckpoint{}, 0, 0, 0
	}
	live := open()
	defer live.Body.Close()
	scanner := bufio.NewScanner(live.Body)
	cursor := initial.Sequence
	readPoint(scanner, &cursor) // The subscriber is live before any summary exists.
	body := strings.Repeat("summary-body-", 104*1024/13)
	body += strings.Repeat("x", 104*1024-len(body))
	for offset := 0; offset < len(body); offset += 2048 {
		if _, err := st.AppendSummaryDelta(ctx, id, run, 3, 0, body[offset:offset+2048]); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.AppendRunEvent(ctx, id, run, "context_notice", map[string]any{"runId": run, "summary": body[:2048], "outputTurn": 3, "outputBytes": len(body)}); err != nil {
		t.Fatal(err)
	}
	finished, err := st.FinishRun(ctx, id, run, "answer", "succeeded", "continued answer")
	if err != nil {
		t.Fatal(err)
	}
	app.hub.notify(id)
	point, chunks, bytes, wireBytes := readPoint(scanner, &cursor)
	if cursor != finished.Sequence || point.Session.Executing || chunks != 52 || bytes != len(body) || wireBytes >= len(body)/2 {
		t.Fatalf("live summary coverage/payload: cursor=%d chunks=%d bytes=%d wire=%d", cursor, chunks, bytes, wireBytes)
	}
	t.Logf("summary: canonical=%d bytes, live reference records=%d bytes, chunks=%d", len(body), wireBytes, chunks)
	replay := open() // A second client catches up from the original boundary.
	defer replay.Body.Close()
	replayCursor := initial.Sequence
	_, replayChunks, replayBytes, _ := readPoint(bufio.NewScanner(replay.Body), &replayCursor)
	if replayCursor != finished.Sequence || replayChunks != 52 || replayBytes != len(body) {
		t.Fatalf("summary replay lost coverage: cursor=%d chunks=%d bytes=%d", replayCursor, replayChunks, replayBytes)
	}
	request := func(path string) *httptest.ResponseRecorder {
		t.Helper()
		response := httptest.NewRecorder()
		app.session(response, httptest.NewRequest(http.MethodGet, "/api/sessions/"+id+path, nil))
		if response.Code != http.StatusOK {
			t.Fatalf("%s: HTTP %d %s", path, response.Code, response.Body.String())
		}
		return response
	}
	for _, path := range []string{"/snapshot", fmt.Sprintf("/event-page?after=%d&through=%d", initial.Sequence, finished.Sequence)} {
		response := request(path)
		var events []store.Event
		if path == "/snapshot" {
			var snapshot store.ConversationSnapshot
			if err := json.Unmarshal(response.Body.Bytes(), &snapshot); err != nil {
				t.Fatal(err)
			}
			events = snapshot.Events
		} else if err := json.Unmarshal(response.Body.Bytes(), &events); err != nil {
			t.Fatal(err)
		}
		bytes := 0
		for _, event := range events {
			if event.Kind == "model_delta" {
				bytes += checkChunk(event, bytes)
			}
		}
		if bytes != len(body) {
			t.Fatalf("%s lost summary references: %d", path, bytes)
		}
	}
	response := request("/runs/" + run + "/model-output?turn=3&full=1")
	var full store.ModelOutputPage
	if err := json.Unmarshal(response.Body.Bytes(), &full); err != nil || full.Content != body || !full.Complete {
		t.Fatalf("canonical summary changed: %v, bytes=%d complete=%v", err, len(full.Content), full.Complete)
	}
}

func TestSSECheckpointFollowsCommittedRecordsAndCompletion(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "vista.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	st.Create(ctx, "s", "Stream")
	st.BeginRun(ctx, "s", "r", "u", "question")
	for i := 0; i < 40; i++ {
		if _, err := st.AppendModelDelta(ctx, "s", "r", 1, 0, "part"); err != nil {
			t.Fatal(err)
		}
	}
	app := &App{store: st, ctx: ctx}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { app.eventStream(w, r, "s") }))
	defer server.Close()
	client := &http.Client{Timeout: 5 * time.Second}
	response, err := client.Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	scanner := bufio.NewScanner(response.Body)
	var last int64
	checkpoints := 0
	readPoint := func() store.SyncCheckpoint {
		t.Helper()
		name := ""
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "event: ") {
				name = strings.TrimPrefix(line, "event: ")
			}
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			data := strings.TrimPrefix(line, "data: ")
			if name == "record" {
				var event store.Event
				if err := json.Unmarshal([]byte(data), &event); err != nil {
					t.Fatal(err)
				}
				if event.Sequence <= last {
					t.Fatal("out of order", event.Sequence, last)
				}
				last = event.Sequence
			} else if name == "sync" {
				var point store.SyncCheckpoint
				if err := json.Unmarshal([]byte(data), &point); err != nil {
					t.Fatal(err)
				}
				if point.ThroughSequence != last {
					t.Fatalf("checkpoint skipped records: %+v, cursor %d", point, last)
				}
				checkpoints++
				return point
			}
		}
		t.Fatal("missing checkpoint", scanner.Err())
		return store.SyncCheckpoint{}
	}
	first := readPoint()
	if last != 41 || !first.Session.Executing {
		t.Fatalf("initial catchup incomplete: %+v", first)
	}
	done, err := st.FinishRun(ctx, "s", "r", "a", "succeeded", "answer")
	if err != nil {
		t.Fatal(err)
	}
	app.hub.notify("s")
	final := readPoint()
	if final.ThroughSequence != done.Sequence || final.Session.Executing || final.Session.LatestReplySequence != done.Sequence || checkpoints != 2 {
		t.Fatalf("completion checkpoint: %+v", final)
	}
	stored, err := st.EventsAfter(ctx, "s", 0, 100)
	if err != nil || len(stored) != 42 {
		t.Fatalf("control frames persisted: %d %v", len(stored), err)
	}
}

func TestSnapshotAPIIncludesOnlyCurrentInputProjection(t *testing.T) {
	app, id := inputQueueFixture(t)
	input, event, err := app.store.QueueInputWithDiagnostics(app.ctx, id, "pending", "Pending text", nil, true, "private-selection", "high", "manual", "quality")
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	app.session(response, httptest.NewRequest(http.MethodGet, "/api/sessions/"+id+"/snapshot", nil))
	var body struct {
		store.SyncCheckpoint
		Inputs []map[string]any `json:"queuedInputs"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if response.Code != 200 || body.ThroughSequence != event.Sequence || body.Session.PendingInputCount != 1 || len(body.Inputs) != 1 {
		t.Fatalf("invalid input snapshot: %d %s", response.Code, response.Body.String())
	}
	if len(body.Inputs[0]) != 3 || body.Inputs[0]["id"] != input.ID || body.Inputs[0]["content"] != input.Content || body.Inputs[0]["status"] != "queued" {
		t.Fatalf("unexpected queue payload: %+v", body.Inputs)
	}
}
