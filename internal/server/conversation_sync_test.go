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
